package util

import (
	"encoding/gob"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"fmt"

	"github.com/gruntwork-io/terragrunt/errors"
	"github.com/mattn/go-zglob"
)

// Return true if the given file exists
func FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Return the canonical version of the given path, relative to the given base path. That is, if the given path is a
// relative path, assume it is relative to the given base path. A canonical path is an absolute path with all relative
// components (e.g. "../") fully resolved, which makes it safe to compare paths as strings.
func CanonicalPath(path string, basePath string) (string, error) {
	if !filepath.IsAbs(path) {
		path = JoinPath(basePath, path)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}

	return CleanPath(absPath), nil
}

// Return the canonical version of the given paths, relative to the given base path. That is, if a given path is a
// relative path, assume it is relative to the given base path. A canonical path is an absolute path with all relative
// components (e.g. "../") fully resolved, which makes it safe to compare paths as strings.
func CanonicalPaths(paths []string, basePath string) ([]string, error) {
	canonicalPaths := []string{}

	for _, path := range paths {
		canonicalPath, err := CanonicalPath(path, basePath)
		if err != nil {
			return canonicalPaths, err
		}
		canonicalPaths = append(canonicalPaths, canonicalPath)
	}

	return canonicalPaths, nil
}

// Returns true if the given regex can be found in any of the files matched by the given glob
func Grep(regex *regexp.Regexp, glob string) (bool, error) {
	// Ideally, we'd use a builin Go library like filepath.Glob here, but per https://github.com/golang/go/issues/11862,
	// the current go implementation doesn't support treating ** as zero or more directories, just zero or one.
	// So we use a third-party library.
	matches, err := zglob.Glob(glob)
	if err != nil {
		return false, errors.WithStackTrace(err)
	}

	for _, match := range matches {
		if IsDir(match) {
			continue
		}
		bytes, err := ioutil.ReadFile(match)
		if err != nil {
			return false, errors.WithStackTrace(err)
		}

		if regex.Match(bytes) {
			return true, nil
		}
	}

	return false, nil
}

// Return true if the path points to a directory
func IsDir(path string) bool {
	fileInfo, err := os.Stat(path)
	return err == nil && fileInfo.IsDir()
}

// Return true if the path points to a file
func IsFile(path string) bool {
	fileInfo, err := os.Stat(path)
	return err == nil && !fileInfo.IsDir()
}

// Return the relative path you would have to take to get from basePath to path
func GetPathRelativeTo(path string, basePath string) (string, error) {
	if path == "" {
		path = "."
	}
	if basePath == "" {
		basePath = "."
	}

	inputFolderAbs, err := filepath.Abs(basePath)
	if err != nil {
		return "", errors.WithStackTrace(err)
	}

	fileAbs, err := filepath.Abs(path)
	if err != nil {
		return "", errors.WithStackTrace(err)
	}

	relPath, err := filepath.Rel(inputFolderAbs, fileAbs)
	if err != nil {
		return "", errors.WithStackTrace(err)
	}

	return filepath.ToSlash(relPath), nil
}

// Return the contents of the file at the given path as a string
func ReadFileAsString(path string) (string, error) {
	bytes, err := ioutil.ReadFile(path)
	if err != nil {
		return "", errors.WithStackTraceAndPrefix(err, "Error reading file at path %s", path)
	}

	return string(bytes), nil
}

// Copy the files and folders within the source folder into the destination folder. Note that hidden files and folders
// (those starting with a dot) will be skipped.
func CopyFolderContents(source, destination, manifestFile string) error {
	return CopyFolderContentsWithFilter(source, destination, manifestFile, func(path string) bool {
		return !PathContainsHiddenFileOrFolder(path)
	})
}

// Copy the files and folders within the source folder into the destination folder. Pass each file and folder through
// the given filter function and only copy it if the filter returns true.
func CopyFolderContentsWithFilter(source, destination, manifestFile string, filter func(path string) bool) error {
	// Why use filepath.Glob here? The original implementation used ioutil.ReadDir, but that method calls lstat on all
	// the files/folders in the directory, including files/folders you may want to explicitly skip. The next attempt
	// was to use filepath.Walk, but that doesn't work because it ignores symlinks. So, now we turn to filepath.Glob.
	manifest := newFileManifest(filepath.Join(destination, manifestFile))
	if err := manifest.Clean(); err != nil {
		return errors.WithStackTrace(err)
	}
	if err := manifest.Create(); err != nil {
		return errors.WithStackTrace(err)
	}

	files, err := filepath.Glob(fmt.Sprintf("%s/*", source))
	if err != nil {
		return errors.WithStackTrace(err)
	}

	for _, file := range files {
		fileRelativePath, err := GetPathRelativeTo(file, source)
		if err != nil {
			return err
		}

		if !filter(fileRelativePath) {
			continue
		}

		dest := filepath.Join(destination, fileRelativePath)

		if IsDir(file) {
			info, err := os.Lstat(file)
			if err != nil {
				return errors.WithStackTrace(err)
			}

			if err := os.MkdirAll(dest, info.Mode()); err != nil {
				return errors.WithStackTrace(err)
			}

			if err := CopyFolderContentsWithFilter(file, dest, manifestFile, filter); err != nil {
				return err
			}
		} else {
			parentDir := filepath.Dir(dest)
			if err := os.MkdirAll(parentDir, 0700); err != nil {
				return errors.WithStackTrace(err)
			}
			if err := CopyFile(file, dest); err != nil {
				return err
			}
			if err := manifest.AddFile(dest); err != nil {
				return err
			}
		}
	}

	return manifest.Close()
}

// IsSymLink returns true if the given file is a symbolic link
// Per https://stackoverflow.com/a/18062079/2308858
func IsSymLink(path string) bool {
	fileInfo, err := os.Lstat(path)
	return err == nil && fileInfo.Mode()&os.ModeSymlink != 0
}

func PathContainsHiddenFileOrFolder(path string) bool {
	pathParts := strings.Split(path, string(filepath.Separator))
	for _, pathPart := range pathParts {
		if strings.HasPrefix(pathPart, ".") && pathPart != "." && pathPart != ".." {
			return true
		}
	}
	return false
}

// Copy a file from source to destination
func CopyFile(source string, destination string) error {
	contents, err := ioutil.ReadFile(source)
	if err != nil {
		return errors.WithStackTrace(err)
	}

	return WriteFileWithSamePermissions(source, destination, contents)
}

// Write a file to the given destination with the given contents using the same permissions as the file at source
func WriteFileWithSamePermissions(source string, destination string, contents []byte) error {
	fileInfo, err := os.Stat(source)
	if err != nil {
		return errors.WithStackTrace(err)
	}

	return ioutil.WriteFile(destination, contents, fileInfo.Mode())
}

// Windows systems use \ as the path separator *nix uses /
// Use this function when joining paths to force the returned path to use / as the path separator
// This will improve cross-platform compatibility
func JoinPath(elem ...string) string {
	return filepath.ToSlash(filepath.Join(elem...))
}

// Use this function when cleaning paths to ensure the returned path uses / as the path separator to improve cross-platform compatibility
func CleanPath(path string) string {
	return filepath.ToSlash(filepath.Clean(path))
}

// Join two paths together with a double-slash between them, as this is what Terraform uses to identify where a "repo"
// ends and a path within the repo begins. Note: The Terraform docs only mention two forward-slashes, so it's not clear
// if on Windows those should be two back-slashes? https://www.terraform.io/docs/modules/sources.html
func JoinTerraformModulePath(modulesFolder string, path string) string {
	cleanModulesFolder := strings.TrimRight(modulesFolder, `/\`)
	cleanPath := strings.TrimLeft(path, `/\`)
	return fmt.Sprintf("%s//%s", cleanModulesFolder, cleanPath)
}

type fileManifest struct {
	Path       string
	encoder    *gob.Encoder
	fileHandle *os.File
}

// Clean will remove all files specified in the manifest
func (f *fileManifest) Clean() error {
	var path string

	// if manifest file doesn't exist, just exit
	if !FileExists(f.Path) {
		return nil
	}
	file, err := os.Open(f.Path)
	if err != nil {
		return err
	}
	decoder := gob.NewDecoder(file)
	// decode paths one by one
	for {
		err = decoder.Decode(&path)
		if err != nil {
			if err == io.EOF {
				break
			} else {
				return err
			}
		}
		if err := os.RemoveAll(path); err != nil {
			return errors.WithStackTrace(err)
		}
	}
	if err := file.Close(); err != nil {
		return errors.WithStackTrace(err)
	}
	// remove the manifest itself
	if err := os.RemoveAll(f.Path); err != nil {
		return errors.WithStackTrace(err)
	}

	return nil
}

func (f *fileManifest) Create() error {
	var err error
	f.fileHandle, err = os.OpenFile(f.Path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	f.encoder = gob.NewEncoder(f.fileHandle)

	return nil
}
func (f *fileManifest) AddFile(file string) error {
	return f.encoder.Encode(file)
}

func (f *fileManifest) Close() error {
	return f.fileHandle.Close()
}

func newFileManifest(path string) *fileManifest {
	return &fileManifest{Path: path}
}
