package versiondata

const (
	defaultProduct = ".speedboat.seagate.com"
	defaultVersion = "v0-dbg"
)

var (
	Product   = defaultProduct
	Version   = defaultVersion
	BuildSHA  string
	BuildTime string
)
