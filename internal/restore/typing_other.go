//go:build !(darwin || linux || freebsd || netbsd || openbsd)

package restore

func ttyEditing(string) (ready, ok bool) { return false, false }
