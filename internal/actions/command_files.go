package actions

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const maxCommandFileBytes = 64 << 10

var commandFileNames = []string{"GITHUB_ENV", "GITHUB_OUTPUT", "GITHUB_PATH", "GITHUB_STEP_SUMMARY"}

type commandFiles struct {
	root      *os.Root
	parent    *os.Root
	directory string
	identity  os.FileInfo
	original  map[string]os.FileInfo
	paths     map[string]string
}

type commandChanges struct {
	env     map[string]string
	outputs map[string]string
	paths   []string
	notes   []Message
}

func prepareCommandFiles(parent *os.Root, step int) (*commandFiles, error) {
	directory := filepath.Join("files", fmt.Sprintf("step-%d", step))
	if err := parent.Mkdir(directory, 0o700); err != nil {
		return nil, err
	}
	root, err := parent.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	files := &commandFiles{root: root, parent: parent, directory: directory, original: map[string]os.FileInfo{}, paths: map[string]string{}}
	files.identity, err = parent.Lstat(directory)
	for _, name := range commandFileNames {
		if err != nil {
			break
		}
		file, openErr := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if openErr != nil {
			err = openErr
			break
		}
		files.original[name], err = file.Stat()
		err = errors.Join(err, file.Close())
		files.paths[name] = filepath.Join(root.Name(), name)
	}
	if err != nil {
		root.Close()
		return nil, err
	}
	return files, nil
}

func (files *commandFiles) read(windows bool) (commandChanges, error) {
	changes := commandChanges{}
	identity, err := files.parent.Lstat(files.directory)
	if err != nil || identity.Mode()&os.ModeSymlink != 0 || !os.SameFile(identity, files.identity) {
		return changes, fmt.Errorf("workflow.command_file: command directory was replaced")
	}
	contents := make(map[string]string, len(commandFileNames))
	for _, name := range commandFileNames {
		value, err := files.readOne(name)
		if err != nil {
			return changes, fmt.Errorf("workflow.command_file: %s: %w", name, err)
		}
		contents[name] = strings.TrimPrefix(value, "\uFEFF")
	}
	changes.env, err = commandPairs(contents["GITHUB_ENV"], true, windows)
	if err != nil {
		return commandChanges{}, fmt.Errorf("workflow.command_file: GITHUB_ENV: %w", err)
	}
	for name := range changes.env {
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "GITHUB_") || strings.HasPrefix(upper, "RUNNER_") || upper == "NODE_OPTIONS" {
			delete(changes.env, name)
			changes.notes = append(changes.notes, Message{Code: "note.ignored_env", Detail: "Ignored protected environment variable " + name})
		}
	}
	changes.outputs, err = commandPairs(contents["GITHUB_OUTPUT"], false, false)
	if err != nil {
		return commandChanges{}, fmt.Errorf("workflow.command_file: GITHUB_OUTPUT: %w", err)
	}
	for _, line := range strings.Split(contents["GITHUB_PATH"], "\n") {
		if line = strings.TrimSuffix(line, "\r"); line != "" {
			changes.paths = append(changes.paths, line)
		}
	}
	if contents["GITHUB_STEP_SUMMARY"] != "" {
		changes.notes = append(changes.notes, Message{Code: "note.summary", Detail: "OwnGit does not show step summaries."})
	}
	return changes, nil
}

func (files *commandFiles) readOne(name string) (string, error) {
	identity, err := files.root.Lstat(name)
	if err != nil {
		return "", err
	}
	if !identity.Mode().IsRegular() || !os.SameFile(identity, files.original[name]) {
		return "", fmt.Errorf("file is not the original regular file")
	}
	file, err := files.root.OpenFile(name, commandReadFlags(), 0)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || !os.SameFile(info, files.original[name]) || info.Size() > maxCommandFileBytes {
		return "", fmt.Errorf("file was replaced or exceeds %d bytes", maxCommandFileBytes)
	}
	if err := singleLink(file, info); err != nil {
		return "", err
	}
	value, err := io.ReadAll(io.LimitReader(file, maxCommandFileBytes+1))
	if err != nil {
		return "", err
	}
	if len(value) > maxCommandFileBytes || !utf8.Valid(value) || strings.IndexByte(string(value), 0) >= 0 {
		return "", fmt.Errorf("invalid or oversized UTF-8 file")
	}
	if current, err := files.root.Lstat(name); err != nil || !os.SameFile(current, info) || !current.Mode().IsRegular() {
		return "", fmt.Errorf("file changed while reading")
	}
	if err := singleLink(file, info); err != nil {
		return "", err
	}
	return string(value), nil
}

func commandPairs(contents string, environment, windows bool) (map[string]string, error) {
	values := map[string]string{}
	lines := strings.Split(contents, "\n")
	for index := 0; index < len(lines); index++ {
		line := strings.TrimSuffix(lines[index], "\r")
		if line == "" {
			continue
		}
		name, value, assignment := strings.Cut(line, "=")
		multilineName, delimiter, multiline := strings.Cut(line, "<<")
		if multiline && (!assignment || strings.Index(line, "<<") < strings.IndexByte(line, '=')) {
			name = multilineName
			if delimiter == "" {
				return nil, fmt.Errorf("empty multiline delimiter")
			}
			start := index + 1
			for index++; index < len(lines) && strings.TrimSuffix(lines[index], "\r") != delimiter; index++ {
			}
			if index == len(lines) {
				return nil, fmt.Errorf("missing multiline delimiter")
			}
			parts := make([]string, 0, index-start)
			for _, part := range lines[start:index] {
				parts = append(parts, strings.TrimSuffix(part, "\r"))
			}
			value = strings.Join(parts, "\n")
		} else if !assignment {
			return nil, fmt.Errorf("expected NAME=value or NAME<<DELIMITER")
		}
		if name == "" || environment && !validEnvironmentName(name) || len(value) > maxEnvironmentValue {
			return nil, fmt.Errorf("invalid or oversized variable %s", name)
		}
		setEnvironment(values, name, value, environment && windows)
	}
	return values, nil
}
