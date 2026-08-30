package sandbox

// ResolveImageWithSource picks the image a run uses (same priority
// order as ResolveImage) and reports where it came from: "flag" (the
// caller's explicit choice), "config" (the per-repo setting, once
// shipyard.yaml lands), or "auto" (auto-detection from the repository
// contents). Callers print the source in their startup audit line.
func ResolveImageWithSource(explicit, repoDir string) (image, source string) {
	if explicit != "" {
		return explicit, "flag"
	}
	if img := imageFromRepoConfig(repoDir); img != "" {
		return img, "config"
	}
	return DetectImage(repoDir), "auto"
}
