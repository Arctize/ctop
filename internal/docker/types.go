package docker

import (
	"strings"
	"time"
)

// Compose labels.
const (
	LabelProject    = "com.docker.compose.project"
	LabelService    = "com.docker.compose.service"
	LabelNumber     = "com.docker.compose.container-number"
	LabelWorkingDir = "com.docker.compose.project.working_dir"
	LabelConfig     = "com.docker.compose.project.config_files"
	LabelOneoff     = "com.docker.compose.oneoff"
)

// Container is an entry of GET /containers/json.
type Container struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	ImageID string            `json:"ImageID"`
	Command string            `json:"Command"`
	Created int64             `json:"Created"`
	State   string            `json:"State"`
	Status  string            `json:"Status"`
	Ports   []Port            `json:"Ports"`
	Labels  map[string]string `json:"Labels"`
	Mounts  []struct {
		Type        string `json:"Type"`
		Name        string `json:"Name"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
	} `json:"Mounts"`
	Health *struct {
		Status string `json:"Status"`
	} `json:"Health"`
	NetworkSettings struct {
		Networks map[string]struct {
			NetworkID string `json:"NetworkID"`
			IPAddress string `json:"IPAddress"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
}

// Name returns the primary container name without the leading slash.
func (c *Container) Name() string {
	for _, n := range c.Names {
		n = strings.TrimPrefix(n, "/")
		if !strings.Contains(n, "/") { // skip legacy link aliases
			return n
		}
	}
	if len(c.Names) > 0 {
		return strings.TrimPrefix(c.Names[0], "/")
	}
	return ShortID(c.ID)
}

// Project returns the compose project, or "".
func (c *Container) Project() string { return c.Labels[LabelProject] }

// Service returns the compose service, or "".
func (c *Container) Service() string { return c.Labels[LabelService] }

// HealthStatus returns healthy/unhealthy/starting or "" when there is no
// health check.
func (c *Container) HealthStatus() string {
	if c.Health != nil && c.Health.Status != "" && c.Health.Status != "none" {
		return c.Health.Status
	}
	switch {
	case strings.Contains(c.Status, "(healthy)"):
		return "healthy"
	case strings.Contains(c.Status, "(unhealthy)"):
		return "unhealthy"
	case strings.Contains(c.Status, "(health: starting)"):
		return "starting"
	}
	return ""
}

// IsUp reports whether the container has live processes.
func (c *Container) IsUp() bool {
	return c.State == "running" || c.State == "paused" || c.State == "restarting"
}

// Port is a container port mapping.
type Port struct {
	IP          string `json:"IP"`
	PrivatePort uint16 `json:"PrivatePort"`
	PublicPort  uint16 `json:"PublicPort"`
	Type        string `json:"Type"`
}

// Stats is the subset of /containers/{id}/stats we use.
type Stats struct {
	Read     time.Time `json:"read"`
	CPUStats struct {
		CPUUsage struct {
			TotalUsage  uint64   `json:"total_usage"`
			PercpuUsage []uint64 `json:"percpu_usage"`
		} `json:"cpu_usage"`
		SystemUsage uint64 `json:"system_cpu_usage"`
		OnlineCPUs  uint32 `json:"online_cpus"`
	} `json:"cpu_stats"`
	MemoryStats struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
	} `json:"networks"`
	BlkioStats struct {
		IoServiceBytesRecursive []struct {
			Op    string `json:"op"`
			Value uint64 `json:"value"`
		} `json:"io_service_bytes_recursive"`
	} `json:"blkio_stats"`
	PidsStats struct {
		Current uint64 `json:"current"`
	} `json:"pids_stats"`
}

// MemUsed returns memory usage the same way `docker stats` does (minus page cache).
func (s *Stats) MemUsed() uint64 {
	u := s.MemoryStats.Usage
	if v, ok := s.MemoryStats.Stats["total_inactive_file"]; ok && v < u { // cgroup v1
		return u - v
	}
	if v, ok := s.MemoryStats.Stats["inactive_file"]; ok && v < u { // cgroup v2
		return u - v
	}
	return u
}

// NetTotals sums rx/tx over all interfaces.
func (s *Stats) NetTotals() (rx, tx uint64) {
	for _, n := range s.Networks {
		rx += n.RxBytes
		tx += n.TxBytes
	}
	return
}

// BlockTotals sums read/write bytes.
func (s *Stats) BlockTotals() (r, w uint64) {
	for _, e := range s.BlkioStats.IoServiceBytesRecursive {
		switch strings.ToLower(e.Op) {
		case "read":
			r += e.Value
		case "write":
			w += e.Value
		}
	}
	return
}

// ContainerJSON is the subset of GET /containers/{id}/json we use.
type ContainerJSON struct {
	ID      string `json:"Id"`
	Name    string `json:"Name"`
	Created string `json:"Created"`
	Path    string `json:"Path"`
	Args    []string
	State   struct {
		Status     string `json:"Status"`
		Running    bool   `json:"Running"`
		Paused     bool   `json:"Paused"`
		Restarting bool   `json:"Restarting"`
		OOMKilled  bool   `json:"OOMKilled"`
		Pid        int    `json:"Pid"`
		ExitCode   int    `json:"ExitCode"`
		Error      string `json:"Error"`
		StartedAt  string `json:"StartedAt"`
		FinishedAt string `json:"FinishedAt"`
		Health     *struct {
			Status        string `json:"Status"`
			FailingStreak int    `json:"FailingStreak"`
		} `json:"Health"`
	} `json:"State"`
	Image        string `json:"Image"`
	RestartCount int    `json:"RestartCount"`
	Config       struct {
		Hostname   string            `json:"Hostname"`
		User       string            `json:"User"`
		Tty        bool              `json:"Tty"`
		OpenStdin  bool              `json:"OpenStdin"`
		Env        []string          `json:"Env"`
		Cmd        []string          `json:"Cmd"`
		Entrypoint []string          `json:"Entrypoint"`
		Image      string            `json:"Image"`
		WorkingDir string            `json:"WorkingDir"`
		Labels     map[string]string `json:"Labels"`
	} `json:"Config"`
	HostConfig struct {
		NetworkMode   string `json:"NetworkMode"`
		RestartPolicy struct {
			Name              string `json:"Name"`
			MaximumRetryCount int    `json:"MaximumRetryCount"`
		} `json:"RestartPolicy"`
		Memory   int64 `json:"Memory"`
		NanoCpus int64 `json:"NanoCpus"`
	} `json:"HostConfig"`
	NetworkSettings struct {
		Ports    map[string][]struct{ HostIp, HostPort string } `json:"Ports"`
		Networks map[string]struct {
			IPAddress  string   `json:"IPAddress"`
			Gateway    string   `json:"Gateway"`
			MacAddress string   `json:"MacAddress"`
			Aliases    []string `json:"Aliases"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
	Mounts []struct {
		Type        string `json:"Type"`
		Name        string `json:"Name"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
		RW          bool   `json:"RW"`
	} `json:"Mounts"`
}

// Image is an entry of GET /images/json.
type Image struct {
	ID          string            `json:"Id"`
	RepoTags    []string          `json:"RepoTags"`
	RepoDigests []string          `json:"RepoDigests"`
	Created     int64             `json:"Created"`
	Size        int64             `json:"Size"`
	Labels      map[string]string `json:"Labels"`
	Containers  int64             `json:"Containers"`
}

// Network is an entry of GET /networks.
type Network struct {
	Name       string            `json:"Name"`
	ID         string            `json:"Id"`
	Created    time.Time         `json:"Created"`
	Scope      string            `json:"Scope"`
	Driver     string            `json:"Driver"`
	Internal   bool              `json:"Internal"`
	Attachable bool              `json:"Attachable"`
	Labels     map[string]string `json:"Labels"`
	IPAM       struct {
		Config []struct {
			Subnet  string `json:"Subnet"`
			Gateway string `json:"Gateway"`
		} `json:"Config"`
	} `json:"IPAM"`
}

// Volume is an entry of GET /volumes.
type Volume struct {
	Name       string            `json:"Name"`
	Driver     string            `json:"Driver"`
	Mountpoint string            `json:"Mountpoint"`
	CreatedAt  string            `json:"CreatedAt"`
	Labels     map[string]string `json:"Labels"`
	Scope      string            `json:"Scope"`
	UsageData  *struct {
		Size     int64 `json:"Size"`
		RefCount int64 `json:"RefCount"`
	} `json:"UsageData"`
}

// Info is the subset of GET /info we use.
type Info struct {
	ID                string   `json:"ID"`
	Name              string   `json:"Name"`
	ServerVersion     string   `json:"ServerVersion"`
	OperatingSystem   string   `json:"OperatingSystem"`
	OSType            string   `json:"OSType"`
	KernelVersion     string   `json:"KernelVersion"`
	Architecture      string   `json:"Architecture"`
	NCPU              int      `json:"NCPU"`
	MemTotal          int64    `json:"MemTotal"`
	Driver            string   `json:"Driver"`
	CgroupDriver      string   `json:"CgroupDriver"`
	CgroupVersion     string   `json:"CgroupVersion"`
	DockerRootDir     string   `json:"DockerRootDir"`
	LoggingDriver     string   `json:"LoggingDriver"`
	DefaultRuntime    string   `json:"DefaultRuntime"`
	Containers        int      `json:"Containers"`
	ContainersRunning int      `json:"ContainersRunning"`
	ContainersPaused  int      `json:"ContainersPaused"`
	ContainersStopped int      `json:"ContainersStopped"`
	Images            int      `json:"Images"`
	SecurityOptions   []string `json:"SecurityOptions"`
	Warnings          []string `json:"Warnings"`
	Swarm             struct {
		LocalNodeState string `json:"LocalNodeState"`
	} `json:"Swarm"`
}

// Version is the subset of GET /version we use.
type Version struct {
	Version       string `json:"Version"`
	APIVersion    string `json:"ApiVersion"`
	MinAPIVersion string `json:"MinAPIVersion"`
	GoVersion     string `json:"GoVersion"`
	Os            string `json:"Os"`
	Arch          string `json:"Arch"`
	Components    []struct {
		Name    string `json:"Name"`
		Version string `json:"Version"`
	} `json:"Components"`
}

// DiskUsage is the subset of GET /system/df we use.
type DiskUsage struct {
	LayersSize int64   `json:"LayersSize"`
	Images     []Image `json:"Images"`
	Containers []struct {
		ID     string `json:"Id"`
		State  string `json:"State"`
		SizeRw int64  `json:"SizeRw"`
	} `json:"Containers"`
	Volumes    []Volume `json:"Volumes"`
	BuildCache []struct {
		Size   int64 `json:"Size"`
		InUse  bool  `json:"InUse"`
		Shared bool  `json:"Shared"`
	} `json:"BuildCache"`
}

// ShortID trims an ID (and sha256: prefix) to 12 characters.
func ShortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
