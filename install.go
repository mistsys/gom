package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type vcsCmd struct {
	checkout     []string
	update       []string
	revision     []string
	revisionMask string
}

var (
	hg = &vcsCmd{
		[]string{"hg", "update"},
		[]string{"hg", "pull"},
		[]string{"hg", "id", "-i"},
		"^(.+)$",
	}
	git = &vcsCmd{
		[]string{"git", "checkout", "-q"},
		[]string{"git", "fetch"},
		[]string{"git", "rev-parse", "HEAD"},
		"^(.+)$",
	}
	bzr = &vcsCmd{
		[]string{"bzr", "revert", "-r"},
		[]string{"bzr", "pull"},
		[]string{"bzr", "log", "-r-1", "--line"},
		"^([0-9]+)",
	}
)

func (vcs *vcsCmd) Checkout(p, destination string) error {
	args := append(vcs.checkout, destination)
	return vcsExec(p, args...)
}

func (vcs *vcsCmd) Update(p string) error {
	return vcsExec(p, vcs.update...)
}

func (vcs *vcsCmd) Revision(dir string) (string, error) {
	args := append(vcs.revision)
	if *verbose {
		fmt.Printf("cd %q && %q\n", dir, args)
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Stderr = os.Stderr
	b, err := cmd.Output()
	if err != nil {
		println(err.Error())
		return "", err
	}
	rev := strings.TrimSpace(string(b))
	if vcs.revisionMask != "" {
		return regexp.MustCompile(vcs.revisionMask).FindString(rev), nil
	}
	return rev, nil
}

func (vcs *vcsCmd) Sync(p, destination string) error {
	err := vcs.Checkout(p, destination)
	if err != nil {
		err = vcs.Update(p)
		if err != nil {
			return err
		}
		err = vcs.Checkout(p, destination)
	}
	return err
}

func vcsExec(dir string, args ...string) error {
	if *verbose {
		fmt.Printf("cd %q && %q\n", dir, args)
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func has(c interface{}, key string) bool {
	switch c := c.(type) {
	case map[string]interface{}:
		_, ok := c[key]
		return ok
	case []string:
		for _, s := range c {
			if s == key {
				return true
			}
		}
	}
	return false
}

func (gom *Gom) Clone(args []string) error {
	vendor, err := filepath.Abs(vendorFolder)
	if err != nil {
		return err
	}
	if command, ok := gom.options["command"].(string); ok {
		target, ok := gom.options["target"].(string)
		if !ok {
			target = gom.name
		}

		srcdir := filepath.Join(vendor, "src", target)
		if err := os.MkdirAll(srcdir, 0755); err != nil {
			return err
		}

		customCmd := strings.Split(command, " ")
		customCmd = append(customCmd, srcdir)

		fmt.Printf("fetching %s (%v)\n", gom.name, customCmd)
		err = run(customCmd, Blue)
		if err != nil {
			return err
		}
	} else if private, ok := gom.options["private"].(string); ok {
		if private == "true" {
			target, ok := gom.options["target"].(string)
			if !ok {
				target = gom.name
			}
			srcdir := filepath.Join(vendor, "src", target)
			if _, err := os.Stat(srcdir); err != nil {
				if err := os.MkdirAll(srcdir, 0755); err != nil {
					return err
				}
				if err := gom.clonePrivate(srcdir); err != nil {
					return err
				}
			} else {
				if err := gom.pullPrivate(srcdir); err != nil {
					return err
				}
			}
		}
	}

	if skipdep, ok := gom.options["skipdep"].(string); ok {
		if skipdep == "true" {
			return nil
		}
	}
	cmdArgs := []string{"go", "get", "-d"}
	if insecure, ok := gom.options["insecure"].(string); ok {
		if insecure == "true" {
			cmdArgs = append(cmdArgs, "-insecure")
		}
	}
	cmdArgs = append(cmdArgs, args...)
	cmdArgs = append(cmdArgs, gom.name)

	// NOTE: there is a problem here. 'go get -d' is going to of course fetch the head of the master branch.
	// That is fine. And it will fetch any dependencies of the master branch (other repos). So far so good.
	// Later we will 'git checkout' the proper commit of each of these repos.
	// However if the commit of the repo which we 'git checkout' has a new (or old, really) dependency
	// then nothing has fetched it. We'll pull it from the non-vendored GOPATH if it is there, and otherwise
	// the build will break.
	// Fixing this is quite a mess. We really should do the 'git checkout' as part of 'go get', after it fetches
	// the repo and before it fetches the dependencies. But that would require 'go get' to take the commit as
	// an argument to pass to the inner 'git clone'.
	// Or we could simulate what 'go get' does (since really the hardcoded 'master/HEAD' in  go get is the root
	// of this. However looking at the output of 'go help importpath' shows that 'go get' does a lot of stuff
	// underneath, and that is stuff I don't want to have to redo.
	// 'go get' is open source. Perhaps I can use its pieces and write my own? The code is all in GOROOT/src/cmd/go/get.go
	// and in 'package main', rather than a library. It's BSD licensed, but it's a lot of code to understand and I'd
	// rather not go further down that path right now.
	// So my next best idea is to redo the 'go get -d' after doing each 'git checkout'. That requires mapping between
	// go packages and git repos, but beyond that it should be doable, though of course rather slow (but so is doing a
	// whole 'go get/git clone' in the first place when just one version is needed.

	// Hey, that gives me a way better idea. I should git clone all the github repos with a fixed commit, then do
	// the 'go get -d' to fetch the missing pieces. That would let me do a quick shallow clone, or even use the
	// auto-generated tarballs from github. That would also allow keeping a cache (dl/) of tarballs locally,
	// which is also something I think gom should permit.

	// Other things to fix:
	//  1) if this package uses other packages in the same repo, those aren't getting vendored. But 'go get' is fetching
	//     the master/HEAD into _vendor/. That leads to nasty collisions, and to leakage.
	//     It seems the fix is to 'cp -r' this repo (not package but repo) into _vendor/ before doing any 'go get', and
	//     when building or running tests use that copy of the packages.
	//     If the outer GOPATH was entirely omitted (so GOPATH was just _vendor/) then we'd notice any missing
	//     dependencies which weren't in the Gomfile.

	// Lastly there is the question of why 'gom install' is different from the other commands in exec.go.
	// I would think all of them need to prepare the _vendor/ in the same way.

	fmt.Printf("downloading %s\n", gom.name)
	return run(cmdArgs, Blue)
}

func (gom *Gom) pullPrivate(srcdir string) (err error) {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := os.Chdir(srcdir); err != nil {
		return err
	}
	defer os.Chdir(cwd)

	fmt.Printf("fetching private repo %s\n", gom.name)
	pullCmd := "git pull origin master"
	pullArgs := strings.Split(pullCmd, " ")
	err = run(pullArgs, Blue)
	if err != nil {
		return
	}

	return
}

func (gom *Gom) clonePrivate(srcdir string) (err error) {
	name := strings.Split(gom.name, "/")
	privateUrl := fmt.Sprintf("git@%s:%s/%s", name[0], name[1], name[2])

	fmt.Printf("fetching private repo %s\n", gom.name)
	cloneCmd := []string{"git", "clone", privateUrl, srcdir}
	err = run(cloneCmd, Blue)
	if err != nil {
		return
	}

	return
}

func (gom *Gom) Checkout() error {
	commit_or_branch_or_tag := ""
	if has(gom.options, "branch") {
		commit_or_branch_or_tag, _ = gom.options["branch"].(string)
	}
	if has(gom.options, "tag") {
		commit_or_branch_or_tag, _ = gom.options["tag"].(string)
	}
	if has(gom.options, "commit") {
		commit_or_branch_or_tag, _ = gom.options["commit"].(string)
	}
	if commit_or_branch_or_tag == "" {
		return nil
	}
	vendor, err := filepath.Abs(vendorFolder)
	if err != nil {
		return err
	}
	p := filepath.Join(vendor, "src")
	target, ok := gom.options["target"].(string)
	if !ok {
		target = gom.name
	}
	for _, elem := range strings.Split(target, "/") {
		var vcs *vcsCmd
		p = filepath.Join(p, elem)
		if isDir(filepath.Join(p, ".git")) {
			vcs = git
		} else if isDir(filepath.Join(p, ".hg")) {
			vcs = hg
		} else if isDir(filepath.Join(p, ".bzr")) {
			vcs = bzr
		}
		if vcs != nil {
			p = filepath.Join(vendor, "src", target)
			fmt.Printf("Checking out ref %s for %s\n", commit_or_branch_or_tag, target)
			return vcs.Sync(p, commit_or_branch_or_tag)
		}
	}
	fmt.Printf("Warning: don't know how to checkout for %v\n", gom.name)
	return errors.New("gom currently support git/hg/bzr for specifying tag/branch/commit")
}

func (gom *Gom) Build(args []string) error {
	installCmd := append([]string{"go", "install"}, args...)
	vendor, err := filepath.Abs(vendorFolder)
	if err != nil {
		return err
	}
	target, ok := gom.options["target"].(string)
	if !ok {
		target = gom.name
	}
	p := filepath.Join(vendor, "src", target)
	return vcsExec(p, installCmd...)
}

func isFile(p string) bool {
	if fi, err := os.Stat(filepath.Join(p)); err == nil && !fi.IsDir() {
		return true
	}
	return false
}

func isDir(p string) bool {
	if fi, err := os.Stat(filepath.Join(p)); err == nil && fi.IsDir() {
		return true
	}
	return false
}

func moveSrcToVendorSrc(vendor string) error {
	vendorSrc := filepath.Join(vendor, "src")
	dirs, err := readdirnames(vendor)
	if err != nil {
		return err
	}
	err = os.MkdirAll(vendorSrc, 0755)
	if err != nil {
		return err
	}
	for _, dir := range dirs {
		if dir == "bin" || dir == "pkg" || dir == "src" {
			continue
		}
		err = os.Rename(filepath.Join(vendor, dir), filepath.Join(vendorSrc, dir))
		if err != nil {
			return err
		}
	}
	return nil
}

func moveSrcToVendor(vendor string) error {
	vendorSrc := filepath.Join(vendor, "src")
	dirs, err := readdirnames(vendorSrc)
	if err != nil {
		return err
	}
	for _, dir := range dirs {
		err = os.Rename(filepath.Join(vendorSrc, dir), filepath.Join(vendor, dir))
		if err != nil {
			return err
		}
	}
	err = os.Remove(vendorSrc)
	if err != nil {
		return err
	}
	return nil
}

func readdirnames(dirname string) ([]string, error) {
	f, err := os.Open(dirname)
	if err != nil {
		return nil, err
	}
	list, err := f.Readdirnames(-1)
	f.Close()
	if err != nil {
		return nil, err
	}
	return list, nil
}

func populate(args []string) ([]Gom, error) {
	allGoms, err := parseGomfile(*gomFileName)
	if err != nil {
		return nil, err
	}
	vendor, err := filepath.Abs(vendorFolder)
	if err != nil {
		return nil, err
	}
	_, err = os.Stat(vendor)
	if err != nil {
		err = os.MkdirAll(vendor, 0755)
		if err != nil {
			return nil, err
		}
	}
	if *verbose {
		fmt.Printf("export GOPATH=%q\n", vendor)
	}
	err = os.Setenv("GOPATH", vendor)
	if err != nil {
		return nil, err
	}
	gobin := filepath.Join(vendor, "bin")
	if *verbose {
		fmt.Printf("export GOBIN=%q\n", gobin)
	}
	err = os.Setenv("GOBIN", gobin)
	if err != nil {
		return nil, err
	}

	// 1. Filter goms to install
	goms := make([]Gom, 0)
	for _, gom := range allGoms {
		if group, ok := gom.options["group"]; ok {
			if !matchEnv(group) {
				continue
			}
		}
		if goos, ok := gom.options["goos"]; ok {
			if !matchOS(goos) {
				continue
			}
		}
		goms = append(goms, gom)
	}

	if go15VendorExperimentEnv {
		err = moveSrcToVendorSrc(vendor)
		if err != nil {
			return nil, err
		}
	}

	// 2. Clone the repositories
	for _, gom := range goms {
		err = gom.Clone(args)
		if err != nil {
			return nil, err
		}
	}

	// 3. Checkout the commit/branch/tag if needed
	for _, gom := range goms {
		err = gom.Checkout()
		if err != nil {
			return nil, err
		}
	}

	return goms, nil
}

func install(args []string) error {
	goms, err := populate(args)
	if err != nil {
		return err
	}

	// 4. Build and install
	for _, gom := range goms {
		if skipdep, ok := gom.options["skipdep"].(string); ok {
			if skipdep == "true" {
				continue
			}
		}
		err = gom.Build(args)
		if err != nil {
			return err
		}
	}

	if go15VendorExperimentEnv {
		vendor, err := filepath.Abs(vendorFolder)
		if err != nil {
			return err
		}
		err = moveSrcToVendor(vendor)
		if err != nil {
			return err
		}
	}

	return nil
}
