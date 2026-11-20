package blob

// IsRasterImageType is the ONE content-type allowlist for image bytes that a
// byte mount later serves same-origin with their stored Content-Type — map
// images, entry portraits, and both again when a Campaign Bundle (an untrusted
// share format, ADR-0053) imports them: PNG, JPEG, WebP or GIF. image/svg+xml,
// text/html and anything else scriptable are deliberately absent, because a
// scriptable type served same-origin is stored XSS. A security allowlist lives
// in one place so a format added to one path can never silently stay refused
// (or worse, allowed) on another.
func IsRasterImageType(ct string) bool {
	switch ct {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
		return true
	}
	return false
}
