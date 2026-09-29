package main

import "owngit/internal/service"

// inContainerImage reports whether this program is the one in the OwnGit
// container image.
func inContainerImage() (bool, error) {
	install, err := detectInstall()
	return install.Route == service.RouteContainer, err
}
