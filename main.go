package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport/ssh"
)

var appPath string = "/tmp/copyright-waiver"

type repo struct {
	Name   string `json:"full_name"`
	SSHURL string `json:"ssh_url"`
	Branch string `json:"default_branch"`
	// I use anonymous struct here since I only care about the key of the
	// license when I pull the info about the repos.
	License struct {
		Key string
	}
	Fork     bool
	Archived bool
}

type license struct {
	Body string
}

var (
	name   = flag.String("name", "", "Specify username.")
	sshKey = flag.String("ssh-key", "", "Specify path to SSH key.")
	usage  = "Usage: \n  ./copyright-waiver -name <YOUR GITHUB USERNAME> -ssh-key <PATH TO YOUR SSH KEY>\n"
)

func main() {
	flag.Parse()

	if *name == "" || *sshKey == "" {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}

	repos := reposBy(*name)
	if len(repos) == 0 {
		fmt.Println("No repos to update under the given username.")
		os.Exit(0)
	}
	publicKey := newPublicKey(*sshKey)
	cloneRepos(repos, publicKey)
	for _, repo := range repos {
		modifyLicense(repo.Name, publicDomainLicense().Body)
		commitChanges(repo.Name)
		pushChanges(repo.Name, publicKey)
	}
	cleanup()
}

func reposBy(username string) []repo {
	// TODO: currently only fetching repos from GitHub, maybe some other hosts
	// could be used too?
	url := fmt.Sprintf("https://api.github.com/users/%s/repos", username)
	resp, err := http.Get(url)
	if err != nil {
		log.Fatal(err)
	}
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}
	repos := []repo{}
	err = json.Unmarshal(body, &repos)
	if err != nil {
		log.Fatal(err)
	}
	filteredRepos := []repo{}
	for _, repo := range repos {
		// It's probably a good idea to not touch forks and also straight ahead
		// ignore repos with public-domain-equivalent license.
		switch {
		case repo.Archived:
		case repo.Fork:
		case repo.License.Key == "unlicense":
		case repo.License.Key == "cc0-1.0":
		case repo.License.Key == "0bsd":
		case repo.License.Key == "wtfpl":
			continue
		default:
			filteredRepos = append(filteredRepos, repo)
		}
	}
	return filteredRepos
}

func newPublicKey(path string) *ssh.PublicKeys {
	normalizedPath := normalizeSSHKeyPath(path)
	_, err := os.Stat(normalizedPath)
	if err != nil {
		log.Fatal(err)
	}
	publicKeys, err := ssh.NewPublicKeysFromFile("git", normalizedPath, "")
	if err != nil {
		log.Fatal(err)
	}
	return publicKeys
}

func normalizeSSHKeyPath(path string) string {
	if strings.Contains(path, "~") {
		home := userHomeDir()
		// if path starts with // it should go to root, but just to be safe
		if home == "/" {
			path = strings.Replace(path, "", home, 1)
			return path
		}
		path = strings.Replace(path, "~", home, 1)
		return path
	} else {
		return path
	}
}

func userHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatal(err)
	}
	return home
}

func cloneRepos(repos []repo, publicKey *ssh.PublicKeys) {
	log.Println("To gracefully stop the clone operation, push Crtl-C.")
	for _, repo := range repos {
		cloneOpts := cloneOpts(repo, publicKey)
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			<-stop
			cancel()
		}()
		_, err := git.PlainCloneContext(
			ctx,
			fmt.Sprintf("%s/%s", appPath, repo.Name),
			false,
			&cloneOpts,
		)
		if err != nil {
			log.Fatal(err)
		}
	}
}

func cloneOpts(r repo, publicKey *ssh.PublicKeys) git.CloneOptions {
	return git.CloneOptions{
		Auth:          publicKey,
		Progress:      os.Stdout,
		RemoteName:    "origin",
		ReferenceName: plumbing.NewBranchReferenceName(r.Branch),
		SingleBranch:  true,
		URL:           r.SSHURL,
	}
}

// Public domain is tricky. For now, I'm using Unlicense since it seems to be
// the most clear public-domain-equivalent license and approved by OSI. Other
// options are 0BSD (technically doesn't waive copyright), WTFPL (both badly
// worded and illegal in some parts of the world) and CC0 (non OSI-approved
// due to patent concerns).
func publicDomainLicense() license {
	resp, err := http.Get("https://api.github.com/licenses/unlicense")
	if err != nil {
		log.Fatal(err)
	}
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}
	license := license{}
	err = json.Unmarshal(body, &license)
	if err != nil {
		log.Fatal(err)
	}
	return license
}

func modifyLicense(repoName string, newLicenseBody string) {
	path := fmt.Sprintf("%s/%s", appPath, repoName)
	licenseFileName := "LICENSE"
	_, err := os.Stat(fmt.Sprintf("%s/LICENSE.txt", path))
	if !errors.Is(err, os.ErrNotExist) {
		licenseFileName = "LICENSE.txt"
	}
	_, err = os.Stat(fmt.Sprintf("%s/LICENSE.md", path))
	if !errors.Is(err, os.ErrNotExist) {
		licenseFileName = "LICENSE.md"
	}
	licensePath := fmt.Sprintf("%s/%s", path, licenseFileName)
	os.Remove(licensePath)
	os.WriteFile(licensePath, []byte(newLicenseBody), 0644)
}

func commitChanges(repoName string) {
	repoDir := fmt.Sprintf("%s/%s", appPath, repoName)
	repo, err := git.PlainOpen(repoDir)
	if err != nil {
		log.Fatal(err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		log.Fatal(err)
	}
	_, err = worktree.Add("LICENSE")
	if err != nil {
		log.Fatal(err)
	}
	msg := "copyright-waiver: Update license to a public-domain-equivalent license"
	commit, err := worktree.Commit(
		msg,
		&git.CommitOptions{},
	)
	if err != nil {
		log.Fatal(err)
	}
	_, err = repo.CommitObject(commit)
	if err != nil {
		log.Fatal(err)
	}
}

func pushChanges(repoName string, publicKey *ssh.PublicKeys) {
	repoDir := fmt.Sprintf("%s/%s", appPath, repoName)
	repo, err := git.PlainOpen(repoDir)
	if err != nil {
		log.Fatal(err)
	}
	pushOpts := pushOpts(publicKey)
	err = repo.Push(&pushOpts)
	if err != nil {
		log.Fatal(err)
	}
}

func pushOpts(publicKey *ssh.PublicKeys) git.PushOptions {
	return git.PushOptions{
		Auth:       publicKey,
		Progress:   os.Stdout,
		RemoteName: "origin",
	}
}

func cleanup() { os.RemoveAll(appPath) }
