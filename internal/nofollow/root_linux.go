//go:build linux

package nofollow

import "golang.org/x/sys/unix"

func openTraversalRoot() (int, error) {
	const flags = unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	fd, err := unix.Open("/", unix.O_RDONLY|flags, 0)
	if err != unix.EACCES {
		return fd, err
	}
	// Keep the same absolute anchor and descriptor-relative traversal. O_PATH
	// does not need directory read permission; openat still enforces search
	// permission. This descriptor is only an intermediate dirfd, never used
	// for reading, publication or fsync. Do not broaden the retry to EPERM or
	// resource errors, or change the anchor to the caller's working directory.
	return unix.Open("/", unix.O_PATH|flags, 0)
}
