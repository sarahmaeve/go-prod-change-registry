package pcrconfig

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"golang.org/x/term"
)

const maxCredentialSize = 64 << 10

// Write atomically stores cfg at path with mode 0600. When replace is false,
// an existing file is preserved.
func Write(path string, cfg File, replace bool) error {
	if err := validateFile(cfg); err != nil {
		return err
	}
	data, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("%w: encode configuration", ErrInvalid)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("%w: create configuration directory %q", ErrInvalid, dir)
	}
	temp, err := os.CreateTemp(dir, ".config.toml-*")
	if err != nil {
		return fmt.Errorf("%w: create temporary configuration file", ErrInvalid)
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()

	if err := writeTemporaryFile(temp, data); err != nil {
		return fmt.Errorf("%w: write temporary configuration file", ErrInvalid)
	}
	if err := installFile(tempPath, path, replace); err != nil {
		return err
	}
	if err := syncDirectory(dir); err != nil {
		return fmt.Errorf("%w: sync configuration directory %q", ErrInvalid, dir)
	}
	return nil
}

// writeTemporaryFile fills temp and closes it on every path, so the caller
// only has to remove it. The credential reaches disk at 0600 because the
// mode is narrowed before any data is written.
func writeTemporaryFile(temp *os.File, data []byte) error {
	defer func() { _ = temp.Close() }()

	if err := temp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temp.Write(data); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	// intentional second close: this one reports a failed flush, and the
	// deferred close then only returns os.ErrClosed.
	return temp.Close()
}

// installFile moves the prepared temporary file into place. Linking rather
// than renaming is what makes a non-replacing write refuse to clobber.
func installFile(tempPath, path string, replace bool) error {
	if replace {
		if err := os.Rename(tempPath, path); err != nil {
			return fmt.Errorf("%w: replace configuration file %q", ErrInvalid, path)
		}
		return nil
	}
	if err := os.Link(tempPath, path); errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%w: configuration file %q already exists", ErrInvalid, path)
	} else if err != nil {
		return fmt.Errorf("%w: create configuration file %q", ErrInvalid, path)
	}
	return nil
}

// ReadCredential reads one credential from stdin, hiding input when stdin is
// a terminal. The credential itself is never included in an error.
func ReadCredential(stdin io.Reader, stderr io.Writer) (string, error) {
	if file, ok := stdin.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		_, _ = fmt.Fprint(stderr, "Credential (email:app-password): ")
		data, err := term.ReadPassword(int(file.Fd()))
		_, _ = fmt.Fprintln(stderr)
		if err != nil {
			return "", fmt.Errorf("%w: read credential from terminal", ErrInvalid)
		}
		credential := string(data)
		if err := ValidateCredential(credential); err != nil {
			return "", err
		}
		return credential, nil
	}

	data, err := io.ReadAll(io.LimitReader(stdin, maxCredentialSize+1))
	if err != nil {
		return "", fmt.Errorf("%w: read credential from stdin", ErrInvalid)
	}
	if len(data) > maxCredentialSize {
		return "", fmt.Errorf("%w: credential input is too large", ErrInvalid)
	}
	credential := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	if err := ValidateCredential(credential); err != nil {
		return "", err
	}
	return credential, nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path) //nolint:gosec // G304: path is the operator-selected configuration directory, opened only to fsync it.
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
