// Package docker is a minimal client for the Docker Engine API over the local
// unix socket. It implements only the endpoints Claude Hub needs (list, inspect,
// stats, lifecycle actions, pull+create, network ensure) to avoid pulling in the
// large official SDK; the project stays dependency-light (cf. the modernc sqlite
// choice). Containers are associated to a project/environment by labels.
package docker

import "strings"

// Label keys tying a container to a Claude Hub project and environment.
const (
	LabelProject = "claudehub.project"     // project slug
	LabelEnv     = "claudehub.environment" // "sandbox" | "live"
	LabelWebPort = "claudehub.web_port"    // container port exposed through the proxy
)

// Port is a published port as reported by /containers/json.
type Port struct {
	IP          string `json:"IP"`
	PrivatePort int    `json:"PrivatePort"`
	PublicPort  int    `json:"PublicPort"`
	Type        string `json:"Type"`
}

// Container is one entry of the container list.
type Container struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	State   string            `json:"State"` // running | exited | created | paused | ...
	Status  string            `json:"Status"`
	Created int64             `json:"Created"`
	Labels  map[string]string `json:"Labels"`
	Ports   []Port            `json:"Ports"`
}

// Name returns the primary container name without the leading slash.
func (c Container) Name() string {
	if len(c.Names) > 0 {
		return strings.TrimPrefix(c.Names[0], "/")
	}
	return ""
}

// Inspect is the subset of /containers/{id}/json we use (port mapping for the
// reverse proxy, restart policy and labels).
type Inspect struct {
	ID    string `json:"Id"`
	Name  string `json:"Name"`
	State struct {
		Running bool   `json:"Running"`
		Status  string `json:"Status"`
	} `json:"State"`
	Config struct {
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	HostConfig struct {
		RestartPolicy struct {
			Name string `json:"Name"`
		} `json:"RestartPolicy"`
	} `json:"HostConfig"`
	NetworkSettings struct {
		Ports map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"Ports"`
	} `json:"NetworkSettings"`
}

// Stats is the subset of /containers/{id}/stats (stream=false) needed to derive
// CPU% and memory usage.
type Stats struct {
	CPU struct {
		CPUUsage struct {
			Total int64 `json:"total_usage"`
		} `json:"cpu_usage"`
		System     int64 `json:"system_cpu_usage"`
		OnlineCPUs int   `json:"online_cpus"`
	} `json:"cpu_stats"`
	PreCPU struct {
		CPUUsage struct {
			Total int64 `json:"total_usage"`
		} `json:"cpu_usage"`
		System int64 `json:"system_cpu_usage"`
	} `json:"precpu_stats"`
	Memory struct {
		Usage int64            `json:"usage"`
		Limit int64            `json:"limit"`
		Stats map[string]int64 `json:"stats"`
	} `json:"memory_stats"`
}

// CPUPercent derives the container's CPU usage from the delta between the two
// samples the daemon returns, scaled by the number of online CPUs (the same
// formula `docker stats` uses).
func (s Stats) CPUPercent() float64 {
	cpuDelta := float64(s.CPU.CPUUsage.Total - s.PreCPU.CPUUsage.Total)
	sysDelta := float64(s.CPU.System - s.PreCPU.System)
	cpus := float64(s.CPU.OnlineCPUs)
	if cpus == 0 {
		cpus = 1
	}
	if cpuDelta > 0 && sysDelta > 0 {
		return (cpuDelta / sysDelta) * cpus * 100
	}
	return 0
}

// MemUsed is the working-set memory: total usage minus the reclaimable page
// cache (inactive_file on cgroup v2, cache on v1), matching `docker stats`.
func (s Stats) MemUsed() int64 {
	if v, ok := s.Memory.Stats["inactive_file"]; ok {
		return s.Memory.Usage - v
	}
	if v, ok := s.Memory.Stats["cache"]; ok {
		return s.Memory.Usage - v
	}
	return s.Memory.Usage
}

// MemLimit is the memory limit reported for the container.
func (s Stats) MemLimit() int64 { return s.Memory.Limit }

// PortSpec is a container port to expose when launching.
type PortSpec struct {
	Container int    // container-side port
	Proto     string // "tcp" | "udp" (defaults to tcp)
}

// RunSpec describes a container to launch.
type RunSpec struct {
	Name          string
	Image         string
	Env           map[string]string
	Ports         []PortSpec
	Labels        map[string]string
	Network       string // "claudehub_sandbox" | "claudehub_live"
	RestartPolicy string // "" | "no" | "unless-stopped" | "always"
	PublishHost   string // host IP to bind published ports to (e.g. 127.0.0.1)
}
