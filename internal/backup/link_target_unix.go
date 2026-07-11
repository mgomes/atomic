//go:build !windows

package backup

func portableLinkTarget(target string) string {
	return target
}
