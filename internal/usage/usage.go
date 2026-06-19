package usage

type PathUsage struct {
	Path           string
	MountSource    string
	MountFSType    string
	CapacityBytes  float64
	AvailableBytes float64
	UsedBytes      float64
}

type ScanStats struct {
	DirectoriesSeen     int64
	FilesStatted        int64
	IgnoredMissingPaths int64
}

type ScanResult struct {
	Usages []PathUsage
	Stats  ScanStats
}
