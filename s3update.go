package s3update

import (
	"archive/tar"
	"compress/gzip"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/mitchellh/ioprogress"
	"golang.org/x/mod/semver"
)

// Updater holds configuration values provided by the program to be updated
type Updater struct {
	CurrentVersion string
	S3VersionKey   string
	ChecksumKey    string
	S3Bucket       string
	S3ReleaseKey   string
	Verbose        bool
}

// validate ensures every required fields is correctly set. Otherwise and error is returned.
func (u Updater) validate() error {
	if u.CurrentVersion == "" {
		return fmt.Errorf("no version set")
	}
	if u.S3Bucket == "" {
		return fmt.Errorf("no bucket set")
	}
	if u.S3ReleaseKey == "" {
		return fmt.Errorf("no s3ReleaseKey set")
	}
	if u.S3VersionKey == "" {
		return fmt.Errorf("no s3VersionKey set")
	}
	return nil
}

// AutoUpdate runs synchronously a verification to ensure the binary is up-to-date.
// If a new version gets released, the download will happen automatically
// It's possible to bypass this mechanism by setting the S3UPDATE_DISABLED environment variable.
func AutoUpdate(u Updater) error {
	if os.Getenv("S3UPDATE_DISABLED") != "" {
		fmt.Println("s3update: autoupdate disabled")
		return nil
	}

	if err := u.validate(); err != nil {
		fmt.Printf("s3update: %s - skipping auto update\n", err.Error())
		return err
	}

	return runAutoUpdate(u)
}

// generateURL composes the download or checksum URL depending on version, os and architecture
func generateURL(bucket, pathTemplate, version string) string {
	p := strings.Replace(pathTemplate, "{{VERSION}}", version, -1)
	p = strings.Replace(p, "{{ARCH}}", runtime.GOARCH, -1)
	p = strings.Replace(p, "{{OS}}", runtime.GOOS, -1)
	return "https://" + bucket + ".s3.amazonaws.com/" + p
}

func fetchRemoteVersion(bucket string) (string, error) {
	resp, err := http.Get("https://" + bucket + ".s3.amazonaws.com/VERSION")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	remoteVersion := strings.TrimSpace(string(body))
	if semver.IsValid(remoteVersion) == false {
		return "", fmt.Errorf("remote version is invalid: %v", remoteVersion)
	}
	return remoteVersion, nil
}

func untgzFile(filename string) error {
	f, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer f.Close()
	r, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(r)
	header, err := tr.Next()
	if err != nil {
		return err
	}
	if header.Typeflag != tar.TypeReg {
		return fmt.Errorf("gunzipping file: unknown file type")
	}
	data, err := ioutil.ReadAll(tr)
	if err != nil {
		return err
	}
	f.Close()
	os.Remove(filename)
	w, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer w.Close()
	_, err = w.Write(data)
	return err
}

func downloadUpdate(downloadURL, checksumURL, version string) error {
	resp, err := http.Get(downloadURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	checksumResp, err := http.Get(checksumURL)
	if err != nil {
		return err
	}
	defer checksumResp.Body.Close()
	checksumRespBody, err := ioutil.ReadAll(checksumResp.Body)
	if err != nil {
		return err
	}

	progressR := &ioprogress.Reader{
		Reader:       resp.Body,
		Size:         resp.ContentLength,
		DrawInterval: 500 * time.Millisecond,
		DrawFunc: ioprogress.DrawTerminalf(os.Stdout, func(progress, total int64) string {
			bar := ioprogress.DrawTextFormatBar(40)
			return fmt.Sprintf("%s %20s", bar(progress, total), ioprogress.DrawTextFormatBytes(progress, total))
		}),
	}

	// follow symlinks
	currentExecutable, err := os.Executable()
	if err != nil {
		return err
	}
	target, err := filepath.EvalSymlinks(currentExecutable)
	if err != nil {
		return err
	}

	// verify target exists, move to backup
	_, err = os.Stat(target)
	if err != nil {
		return nil
	}
	backup := target + ".bak"
	os.Rename(target, backup)

	// use the same flags that ioutil.WriteFile uses
	f, err := os.OpenFile(target, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		os.Rename(backup, target)
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, progressR); err != nil {
		os.Rename(backup, target)
		return err
	}
	f.Close()

	f, err = os.Open(target)
	if err != nil {
		return err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		os.Rename(backup, target)
		return err
	}
	if string(checksumRespBody) != hex.EncodeToString(h.Sum(nil)) {
		os.Rename(backup, target)
		return fmt.Errorf("%s checksum mismatch", version)
	}

	if strings.HasSuffix(downloadURL, ".tgz") {
		err = untgzFile(target)
		if err != nil {
			os.Rename(backup, target)
			return err
		}
	}

	err = os.Chmod(target, 0755)
	if err != nil {
		os.Rename(backup, target)
		return err
	}

	os.Remove(backup)

	fmt.Printf("successfully updated to %s\n", version)

	// re-run original command
	return syscall.Exec(target, os.Args, os.Environ())
}

func runAutoUpdate(u Updater) error {
	if !semver.IsValid(u.CurrentVersion) {
		return fmt.Errorf("invalid local version")
	}
	localVersion := u.CurrentVersion
	remoteVersion, err := fetchRemoteVersion(u.S3Bucket)
	if err != nil {
		return err
	}
	if semver.Compare(localVersion, remoteVersion) == -1 {
		fmt.Printf("upgrading from %s to %s\n", localVersion, remoteVersion)
		downloadURL := generateURL(u.S3Bucket, u.S3ReleaseKey, remoteVersion)
		checksumURL := generateURL(u.S3Bucket, u.ChecksumKey, remoteVersion)
		if u.Verbose {
			fmt.Printf("downloadURL: %s\n", downloadURL)
			fmt.Printf("checksumURL: %s\n", checksumURL)
		}
		err = downloadUpdate(downloadURL, checksumURL, remoteVersion)
		if err != nil {
			return err
		}
		os.Exit(0)
	}
	if u.Verbose {
		fmt.Printf("updater: using the latest version: %s\n", u.CurrentVersion)
	}
	return nil
}
