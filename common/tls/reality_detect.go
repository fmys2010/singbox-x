package tls

// IsRealityClient reports whether config is a REALITY client config.
func IsRealityClient(config Config) bool {
	_, isReality := config.(interface{ IsReality() })
	return isReality
}
