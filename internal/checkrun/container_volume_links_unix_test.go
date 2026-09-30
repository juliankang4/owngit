//go:build !windows

package checkrun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An image volume whose path, or a folder on the way to it, is a link in
// the image is refused: Docker would follow the link when it mounts the
// disposable volume. Paths that are real folders, or absent, are accepted.
func TestImageVolumeThroughALinkIsRefused(t *testing.T) {
	image := t.TempDir()
	noErr(t, os.Symlink("/workspace", filepath.Join(image, "data")))
	noErr(t, os.MkdirAll(filepath.Join(image, "opt", "cache"), 0o755))
	noErr(t, os.Symlink("/workspace", filepath.Join(image, "var")))
	// The stand-in archives the named path of a synthetic image root the way
	// docker cp does: a link as a link, a folder with its contents.
	docker := fakeDocker(t, `
case "$4" in
container:*) target=${4#container:} ;;
*) echo "unexpected $*" >&2; exit 2 ;;
esac
if [ ! -e "`+image+`$target" ] && [ ! -L "`+image+`$target" ]; then
  echo "Error response from daemon: Could not find the file $target in container container" >&2; exit 1
fi
cd "`+image+`$(dirname "$target")" && COPYFILE_DISABLE=1 tar -cf - "$(basename "$target")"`)

	for name, test := range map[string]struct {
		volumes []string
		refused string
	}{
		"link":                  {[]string{"/data"}, "/data is a link"},
		"folder through link":   {[]string{"/var/cache"}, "/var is a link"},
		"real folder":           {[]string{"/opt/cache"}, ""},
		"absent":                {[]string{"/srv/data"}, ""},
		"link after a real one": {[]string{"/opt/cache", "/data"}, "/data is a link"},
	} {
		t.Run(name, func(t *testing.T) {
			err := verifyImageVolumePaths(context.Background(), docker, "unix:///docker.sock", "container", test.volumes)
			if test.refused == "" {
				noErr(t, err)
			} else if err == nil || !strings.Contains(err.Error(), test.refused) {
				t.Fatalf("volumes %v: err=%v, want %q", test.volumes, err, test.refused)
			}
		})
	}
}
