// Package nofollow contains descriptor-relative filesystem primitives shared by
// transcode transactions and received-instance persistence. It refuses symbolic
// links and reparse traversal. Callers own directory trust, entry mutation,
// publication policy, cleanup, and durability; this is not an application sandbox.
package nofollow
