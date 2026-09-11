//go:build aix || darwin || dragonfly || freebsd || netbsd || openbsd || solaris

package nofollow

import "golang.org/x/sys/unix"

func openTraversalRoot() (int, error) {
	return unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
}
