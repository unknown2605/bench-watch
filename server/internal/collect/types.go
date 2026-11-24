// Package collect holds the sample types, status and history sink shared by the machine and Docker collectors.
// Every field is a number, a fixed word or a name that passed the NameGate: no paths, commands, labels or IDs.
package collect

import "time"

type CoreKind string

const (
	CoreP       CoreKind = "P"
	CoreE       CoreKind = "E"
	CoreUnknown CoreKind = "?"
)

type Core struct {
	Index int      `json:"index"`
	Kind  CoreKind `json:"kind"`
	Busy  float64  `json:"busy"`
}

type Load struct {
	One     float64 `json:"one"`
	Five    float64 `json:"five"`
	Fifteen float64 `json:"fifteen"`
}

// Memory follows Activity Monitor: Used = App + Wired + Compressed. When the sample lists PartSwap in
// Missing, SwapTotal and SwapUsed are 0 and must not be read.
type Memory struct {
	Total      uint64 `json:"total"`
	Used       uint64 `json:"used"`
	App        uint64 `json:"app"`
	Wired      uint64 `json:"wired"`
	Compressed uint64 `json:"compressed"`
	Cached     uint64 `json:"cached"`
	Free       uint64 `json:"free"`
	SwapTotal  uint64 `json:"swapTotal"`
	SwapUsed   uint64 `json:"swapUsed"`
}

// Count, on Volume, DiskIO, NetIO, App and Container, is how many names were merged into the Other row: names
// the gate refused and rows past MaxRows. Only the Other row has one, and a list holds at most one Other row.
type Volume struct {
	Name  string `json:"name"`
	Total uint64 `json:"total"`
	Free  uint64 `json:"free"`
	Count int    `json:"count,omitempty"`
	// Kind is "internal" for the boot volume and empty when unknown; Filesystem is the lower-case type name.
	Kind       string `json:"kind,omitempty"`
	Filesystem string `json:"filesystem,omitempty"`
}

type DiskIO struct {
	Name     string  `json:"name"`
	ReadBps  float64 `json:"readBps"`
	WriteBps float64 `json:"writeBps"`
	ReadOps  float64 `json:"readOps"`
	WriteOps float64 `json:"writeOps"`
	Count    int     `json:"count,omitempty"`
}

type NetIO struct {
	Name  string  `json:"name"`
	RxBps float64 `json:"rxBps"`
	TxBps float64 `json:"txBps"`
	Count int     `json:"count,omitempty"`
}

// App groups processes by name; CPU is percent of one core, so it can pass 100.
type App struct {
	Name  string  `json:"name"`
	CPU   float64 `json:"cpu"`
	RSS   uint64  `json:"rss"`
	Procs int     `json:"procs"`
	Count int     `json:"count,omitempty"`
}

// MachineSample: a part in Missing failed, wholly or for some items. Load or Memory is nil when its own part failed
// (PartSwap leaves Memory), a list holds only the items read, and AppsWindow is 0 when no top apps are carried.
type MachineSample struct {
	At         time.Time `json:"at"`
	Interval   float64   `json:"interval"`
	Cores      []Core    `json:"cores"`
	Load       *Load     `json:"load"`
	Memory     *Memory   `json:"memory"`
	Volumes    []Volume  `json:"volumes,omitempty"`
	DiskIO     []DiskIO  `json:"diskIO,omitempty"`
	Net        []NetIO   `json:"net,omitempty"`
	TopCPU     []App     `json:"topCPU,omitempty"`
	TopMem     []App     `json:"topMem,omitempty"`
	AppsWindow float64   `json:"appsWindow,omitempty"`
	DockerVM   *App      `json:"dockerVM,omitempty"`
	Missing    []string  `json:"missing,omitempty"`
}

func (s MachineSample) Lacks(part string) bool { return contains(s.Missing, part) }

// Container: Missing holds PartStats when the stats call failed and PartRates when CPU and network
// rates could not be derived; those numbers are then 0 and must not be read.
type Container struct {
	Name      string  `json:"name"`
	Stack     string  `json:"stack,omitempty"`
	State     string  `json:"state"`
	Health    string  `json:"health,omitempty"`
	CPU       float64 `json:"cpu"`
	MemUsed   uint64  `json:"memUsed"`
	MemLimit  uint64  `json:"memLimit"`
	RxBps     float64 `json:"rxBps"`
	TxBps     float64 `json:"txBps"`
	Restarts  int     `json:"restarts"`
	ExitCode  *int32  `json:"exitCode,omitempty"`
	UptimeSec int64   `json:"uptimeSec"`
	// UptimeApprox is set when uptime comes from Docker's rounded status text, not a start event.
	UptimeApprox bool `json:"uptimeApprox,omitempty"`
	// StackFull: "only" when stack_full is the container's one network, "shared" with others, "" when absent.
	StackFull string   `json:"stackFull,omitempty"`
	Count     int      `json:"count,omitempty"`
	Missing   []string `json:"missing,omitempty"`
}

type DockerVM struct {
	NCPU     int    `json:"ncpu"`
	MemTotal uint64 `json:"memTotal"`
	// MemUsed and CPU sum the containers with stats; the VM's own use and swap are not in the Docker API.
	MemUsed  uint64  `json:"memUsed"`
	CPU      float64 `json:"cpu"`
	SwapUsed *uint64 `json:"swapUsed"`
}

type DockerDisk struct {
	At          time.Time `json:"at"`
	Images      int64     `json:"images"`
	ImageCount  int64     `json:"imageCount"`
	Volumes     int64     `json:"volumes"`
	VolumeCount int64     `json:"volumeCount"`
	BuildCache  int64     `json:"buildCache"`
	Containers  int64     `json:"containers"`
	// Reclaimable and unused figures are the daemon's own; LargestImage is the biggest image's size only, and nil
	// until the image list was read once.
	ImagesReclaimable     int64  `json:"imagesReclaimable"`
	ImagesUnused          int64  `json:"imagesUnused"`
	LargestImage          *int64 `json:"largestImage,omitempty"`
	VolumesReclaimable    int64  `json:"volumesReclaimable"`
	VolumesUnused         int64  `json:"volumesUnused"`
	BuildCacheReclaimable int64  `json:"buildCacheReclaimable"`
	// Reclaimable adds stopped containers to the three kinds above.
	Reclaimable int64 `json:"reclaimable"`
}

type StackFull struct {
	RxBps float64 `json:"rxBps"`
	TxBps float64 `json:"txBps"`
	// Attributed counts containers whose only network is stack_full; Shared ones are left out of the rates.
	Attributed int `json:"attributed"`
	Shared     int `json:"shared"`
}

// DockerSample: Missing lists parts never measured (PartInfo, PartDisk), PartStats or PartRates when a running
// container lacks them, so VM and stack_full sums leave it out; Stale: parts whose last refresh failed.
type DockerSample struct {
	At         time.Time   `json:"at"`
	Interval   float64     `json:"interval"`
	Containers []Container `json:"containers"`
	VM         DockerVM    `json:"vm"`
	Disk       *DockerDisk `json:"disk,omitempty"`
	StackFull  StackFull   `json:"stackFull"`
	Missing    []string    `json:"missing,omitempty"`
	Stale      []string    `json:"stale,omitempty"`
}

func (s DockerSample) Lacks(part string) bool { return contains(s.Missing, part) }

type DockerEvent struct {
	At   time.Time `json:"at"`
	Name string    `json:"name"`
	Kind string    `json:"kind"`
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
