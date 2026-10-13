package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func init() { register(proxmox{}) }

// proxmox talks to the Proxmox VE REST API using an API token (no ticket/CSRF
// dance needed). Homelab nodes usually present a self-signed cert, hence the
// verify_tls opt-out.
type proxmox struct{}

func (proxmox) Type() string  { return "proxmox" }
func (proxmox) Label() string { return "Proxmox VE" }

func (proxmox) Fields() []Field {
	return []Field{
		{Key: "base_url", Label: "URL de l'API", Type: FieldURL, Required: true,
			Placeholder: "https://proxmox.local:8006"},
		{Key: "token_id", Label: "Token ID", Type: FieldText, Required: true,
			Placeholder: "user@pam!mytoken"},
		{Key: "token_secret", Label: "Token secret", Type: FieldPassword, Secret: true, Required: true},
		{Key: "verify_tls", Label: "Vérifier le certificat TLS", Type: FieldBool},
	}
}

func (proxmox) Test(ctx context.Context, cfg map[string]string) error {
	if err := missing(cfg, "base_url", "token_id", "token_secret"); err != nil {
		return err
	}
	var res struct {
		Data struct {
			Version string `json:"version"`
		} `json:"data"`
	}
	return pveGet(ctx, cfg, "/version", &res)
}

func (proxmox) Tools() []ToolSpec {
	vmid := ArgSpec{Name: "vmid", Type: "integer", Description: "VM id", Required: true}
	snapName := ArgSpec{Name: "name", Type: "string", Description: "Snapshot name", Required: true}
	return []ToolSpec{
		{Name: "proxmox_list_vms", Description: "List the VMs known to the Proxmox cluster (id, name, status, node)."},
		{Name: "proxmox_start_vm", Description: "Start a VM by id.", Args: []ArgSpec{vmid}},
		{Name: "proxmox_stop_vm", Description: "Stop a VM by id.", Args: []ArgSpec{vmid}},
		{Name: "proxmox_list_snapshots", Description: "List the snapshots of a VM.", Args: []ArgSpec{vmid}},
		{Name: "proxmox_snapshot_create", Description: "Create a disk-only snapshot of a VM (fast rollback point). Waits for the task to finish.",
			Args: []ArgSpec{vmid, {Name: "name", Type: "string", Description: "Snapshot name (default auto-<timestamp>)"}}},
		{Name: "proxmox_snapshot_rollback", Description: "Roll a VM back to a snapshot. The VM must be stopped first (call proxmox_stop_vm). Waits for the task to finish.",
			Args: []ArgSpec{vmid, snapName}},
		{Name: "proxmox_snapshot_delete", Description: "Delete a VM snapshot. Waits for the task to finish.", Args: []ArgSpec{vmid, snapName}},
		{Name: "proxmox_list_storages", Description: "List available storages on a Proxmox node (id, type, content, status, used/total GB). Useful to pick a target for proxmox_backup_vm.",
			Args: []ArgSpec{{Name: "node", Type: "string", Description: "Node name (e.g. pve). If omitted, uses the first online node."}}},
		backupReportSpec(),
		{Name: "proxmox_backup_vm", Description: "Create a VM backup (vzdump) on a given storage. Snapshot mode is used by default (online, no downtime). Waits for the task to finish.",
			Args: []ArgSpec{
				vmid,
				{Name: "storage", Type: "string", Description: "Target storage name (see proxmox_list_storages).", Required: true},
				{Name: "mode", Type: "string", Description: "Backup mode: snapshot (default, online), suspend, or stop."},
				{Name: "compress", Type: "string", Description: "Compression: zstd (default), lzo, gzip, or 0 for none."},
				{Name: "notes", Type: "string", Description: "Optional note to attach to the backup."},
			}},
	}
}

func (proxmox) Call(ctx context.Context, cfg map[string]string, action string, args map[string]any) (string, error) {
	switch action {
	case "proxmox_list_vms":
		vms, err := pveListVMs(ctx, cfg)
		if err != nil {
			return "", err
		}
		if len(vms) == 0 {
			return "No VMs found.", nil
		}
		var b strings.Builder
		for _, v := range vms {
			fmt.Fprintf(&b, "- %d %q — %s (node %s)\n", v.VMID, v.Name, v.Status, v.Node)
		}
		return strings.TrimSpace(b.String()), nil
	case "proxmox_start_vm", "proxmox_stop_vm":
		vmid, ok := argInt(args, "vmid")
		if !ok {
			return "", fmt.Errorf("vmid is required")
		}
		node, err := pveNodeForVM(ctx, cfg, vmid)
		if err != nil {
			return "", err
		}
		verb := "start"
		if action == "proxmox_stop_vm" {
			verb = "stop"
		}
		path := fmt.Sprintf("/nodes/%s/qemu/%d/status/%s", node, vmid, verb)
		if err := pvePost(ctx, cfg, path); err != nil {
			return "", err
		}
		return fmt.Sprintf("VM %d %s requested on node %s", vmid, verb, node), nil
	case "proxmox_list_snapshots":
		vmid, ok := argInt(args, "vmid")
		if !ok {
			return "", fmt.Errorf("vmid is required")
		}
		node, err := pveNodeForVM(ctx, cfg, vmid)
		if err != nil {
			return "", err
		}
		var res struct {
			Data []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				SnapTime    int64  `json:"snaptime"`
			} `json:"data"`
		}
		if err := pveGet(ctx, cfg, fmt.Sprintf("/nodes/%s/qemu/%d/snapshot", node, vmid), &res); err != nil {
			return "", err
		}
		var b strings.Builder
		for _, s := range res.Data {
			if s.Name == "current" { // the pseudo-snapshot for the live state
				continue
			}
			when := ""
			if s.SnapTime > 0 {
				when = " @ " + time.Unix(s.SnapTime, 0).UTC().Format("2006-01-02 15:04Z")
			}
			fmt.Fprintf(&b, "- %s%s\n", s.Name, when)
		}
		if b.Len() == 0 {
			return fmt.Sprintf("VM %d has no snapshots.", vmid), nil
		}
		return strings.TrimSpace(b.String()), nil
	case "proxmox_snapshot_create":
		vmid, ok := argInt(args, "vmid")
		if !ok {
			return "", fmt.Errorf("vmid is required")
		}
		name := argString(args, "name", "")
		if name == "" {
			name = "auto-" + time.Now().UTC().Format("20060102-150405")
		}
		node, err := pveNodeForVM(ctx, cfg, vmid)
		if err != nil {
			return "", err
		}
		upid, err := pvePostData(ctx, cfg, fmt.Sprintf("/nodes/%s/qemu/%d/snapshot?snapname=%s", node, vmid, url.QueryEscape(name)))
		if err != nil {
			return "", err
		}
		if err := pveWaitTask(ctx, cfg, node, upid); err != nil {
			return "", err
		}
		return fmt.Sprintf("Snapshot %q created on VM %d (node %s)", name, vmid, node), nil
	case "proxmox_snapshot_rollback", "proxmox_snapshot_delete":
		vmid, ok := argInt(args, "vmid")
		if !ok {
			return "", fmt.Errorf("vmid is required")
		}
		name := argString(args, "name", "")
		if name == "" {
			return "", fmt.Errorf("name is required")
		}
		node, err := pveNodeForVM(ctx, cfg, vmid)
		if err != nil {
			return "", err
		}
		var upid string
		if action == "proxmox_snapshot_rollback" {
			upid, err = pvePostData(ctx, cfg, fmt.Sprintf("/nodes/%s/qemu/%d/snapshot/%s/rollback", node, vmid, url.PathEscape(name)))
		} else {
			upid, err = pveDeleteData(ctx, cfg, fmt.Sprintf("/nodes/%s/qemu/%d/snapshot/%s", node, vmid, url.PathEscape(name)))
		}
		if err != nil {
			return "", err
		}
		if err := pveWaitTask(ctx, cfg, node, upid); err != nil {
			return "", err
		}
		if action == "proxmox_snapshot_rollback" {
			return fmt.Sprintf("VM %d rolled back to snapshot %q (node %s)", vmid, name, node), nil
		}
		return fmt.Sprintf("Snapshot %q deleted on VM %d (node %s)", name, vmid, node), nil
	case "proxmox_list_storages":
		node := argString(args, "node", "")
		if node == "" {
			var nodesRes struct {
				Data []struct {
					Node   string `json:"node"`
					Status string `json:"status"`
				} `json:"data"`
			}
			if err := pveGet(ctx, cfg, "/nodes", &nodesRes); err != nil {
				return "", err
			}
			for _, n := range nodesRes.Data {
				if n.Status == "online" {
					node = n.Node
					break
				}
			}
			if node == "" {
				return "", fmt.Errorf("no online Proxmox node found")
			}
		}
		var sres struct {
			Data []struct {
				Storage string `json:"storage"`
				Type    string `json:"type"`
				Content string `json:"content"`
				Status  string `json:"status"`
				Used    int64  `json:"used"`
				Total   int64  `json:"total"`
				Enabled int    `json:"enabled"`
			} `json:"data"`
		}
		if err := pveGet(ctx, cfg, "/nodes/"+node+"/storage", &sres); err != nil {
			return "", err
		}
		if len(sres.Data) == 0 {
			return fmt.Sprintf("No storage found on node %s.", node), nil
		}
		var b strings.Builder
		for _, s := range sres.Data {
			enabled := "enabled"
			if s.Enabled == 0 {
				enabled = "disabled"
			}
			usedGB := float64(s.Used) / 1e9
			totalGB := float64(s.Total) / 1e9
			fmt.Fprintf(&b, "- %s (%s) content=%s %s %.1f/%.1f GB %s\n",
				s.Storage, s.Type, s.Content, s.Status, usedGB, totalGB, enabled)
		}
		return strings.TrimSpace(b.String()), nil
	case "proxmox_backup_report":
		return proxmoxBackupReport(ctx, cfg, args)
	case "proxmox_backup_vm":
		vmid, ok := argInt(args, "vmid")
		if !ok {
			return "", fmt.Errorf("vmid is required")
		}
		storage := argString(args, "storage", "")
		if storage == "" {
			return "", fmt.Errorf("storage is required")
		}
		node, err := pveNodeForVM(ctx, cfg, vmid)
		if err != nil {
			return "", err
		}
		mode := argString(args, "mode", "snapshot")
		compress := argString(args, "compress", "zstd")
		notes := argString(args, "notes", "")
		path := fmt.Sprintf("/nodes/%s/vzdump?vmid=%d&storage=%s&mode=%s&compress=%s",
			node, vmid, url.QueryEscape(storage), url.QueryEscape(mode), url.QueryEscape(compress))
		if notes != "" {
			path += "&notes-template=" + url.QueryEscape(notes)
		}
		upid, err := pvePostData(ctx, cfg, path)
		if err != nil {
			return "", err
		}
		if err := pveWaitTask(ctx, cfg, node, upid); err != nil {
			return "", err
		}
		return fmt.Sprintf("VM %d backed up to storage %q on node %s (mode: %s, compress: %s).", vmid, storage, node, mode, compress), nil
	default:
		return "", fmt.Errorf("unknown action %q", action)
	}
}

// pvePostData issues a POST that returns a Proxmox task id (UPID) in "data".
func pvePostData(ctx context.Context, cfg map[string]string, path string) (string, error) {
	var res struct {
		Data string `json:"data"`
	}
	if err := pveDo(ctx, cfg, http.MethodPost, path, &res); err != nil {
		return "", err
	}
	return res.Data, nil
}

// pveDeleteData issues a DELETE that returns a Proxmox task id (UPID) in "data".
func pveDeleteData(ctx context.Context, cfg map[string]string, path string) (string, error) {
	var res struct {
		Data string `json:"data"`
	}
	if err := pveDo(ctx, cfg, http.MethodDelete, path, &res); err != nil {
		return "", err
	}
	return res.Data, nil
}

// pveWaitTask polls a Proxmox worker task (UPID) until it stops, returning an error
// if it did not exit "OK". Snapshot create/rollback/delete are asynchronous, so the
// caller must wait before assuming the point-in-time state is consistent.
func pveWaitTask(ctx context.Context, cfg map[string]string, node, upid string) error {
	if upid == "" || upid == "OK" {
		return nil // nothing asynchronous to wait on
	}
	path := "/nodes/" + node + "/tasks/" + url.PathEscape(upid) + "/status"
	for i := 0; i < 150; i++ { // ~5 min at a 2 s cadence
		var res struct {
			Data struct {
				Status     string `json:"status"`
				ExitStatus string `json:"exitstatus"`
			} `json:"data"`
		}
		if err := pveGet(ctx, cfg, path, &res); err != nil {
			return err
		}
		if res.Data.Status == "stopped" {
			if res.Data.ExitStatus == "OK" {
				return nil
			}
			return fmt.Errorf("proxmox task failed: %s", res.Data.ExitStatus)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("proxmox task %s did not finish within 5 min", upid)
}

// Dashboard implements Dashboarder. It aggregates per-node endpoints rather than
// /cluster/resources: the latter is empty on a standalone (non-clustered) node and
// is heavily permission-filtered, which silently yields misleading zeros. We read
// /nodes, then each online node's /status (CPU/RAM), /storage and /qemu + /lxc.
// Read access needs the PVEAuditor role (Sys.Audit / VM.Audit / Datastore.Audit);
// when a node's stats are refused we attach a Warning instead of faking zeros.
func (proxmox) Dashboard(ctx context.Context, cfg map[string]string) (any, error) {
	if err := missing(cfg, "base_url", "token_id", "token_secret"); err != nil {
		return nil, err
	}
	var nodesRes struct {
		Data []struct {
			Node   string `json:"node"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := pveGet(ctx, cfg, "/nodes", &nodesRes); err != nil {
		return nil, err
	}

	var d pveDashboard
	var cpuWeighted float64 // Σ(cpu_i · cores_i), normalised by total cores below
	seenStorage := map[string]bool{}
	var warns []string

	for _, n := range nodesRes.Data {
		ns := pveNodeStat{Name: n.Node, Status: n.Status}
		if n.Status != "online" {
			d.Nodes = append(d.Nodes, ns)
			continue
		}

		// Canonical CPU/RAM for the node (permission-gated: Sys.Audit).
		var st struct {
			Data struct {
				CPU     float64 `json:"cpu"` // 0..1 fraction of the node's cores
				CPUInfo struct {
					CPUs int `json:"cpus"`
				} `json:"cpuinfo"`
				Memory struct {
					Used  int64 `json:"used"`
					Total int64 `json:"total"`
				} `json:"memory"`
			} `json:"data"`
		}
		if err := pveGet(ctx, cfg, "/nodes/"+n.Node+"/status", &st); err != nil {
			warns = append(warns, fmt.Sprintf("%s (%v)", n.Node, err))
		} else {
			d.CPU.Cores += st.Data.CPUInfo.CPUs
			cpuWeighted += st.Data.CPU * float64(st.Data.CPUInfo.CPUs)
			d.Memory.Used += st.Data.Memory.Used
			d.Memory.Total += st.Data.Memory.Total
			ns.CPU, ns.Mem, ns.MaxMem = st.Data.CPU, st.Data.Memory.Used, st.Data.Memory.Total
		}
		d.Nodes = append(d.Nodes, ns)

		// Storage: shared stores (NFS/Ceph) report identically on every node, so
		// count them once by name; local stores are distinct per node.
		var sres struct {
			Data []struct {
				Storage string `json:"storage"`
				Used    int64  `json:"used"`
				Total   int64  `json:"total"`
				Shared  int    `json:"shared"`
				Enabled int    `json:"enabled"`
			} `json:"data"`
		}
		if err := pveGet(ctx, cfg, "/nodes/"+n.Node+"/storage", &sres); err == nil {
			for _, s := range sres.Data {
				if s.Storage == "" || s.Enabled == 0 || s.Total == 0 {
					continue
				}
				key := s.Storage
				if s.Shared == 0 {
					key = n.Node + "/" + s.Storage
				}
				if seenStorage[key] {
					continue
				}
				seenStorage[key] = true
				d.Storage.Used += s.Used
				d.Storage.Total += s.Total
			}
		}

		countGuests(ctx, cfg, "/nodes/"+n.Node+"/qemu", &d.VMs)
		countGuests(ctx, cfg, "/nodes/"+n.Node+"/lxc", &d.Containers)
	}

	if d.CPU.Cores > 0 {
		d.CPU.Usage = cpuWeighted / float64(d.CPU.Cores)
	}
	if len(warns) > 0 {
		d.Warning = "Statistiques nœud indisponibles (le token a-t-il le rôle PVEAuditor ?) : " +
			strings.Join(warns, " ; ")
	}
	return d, nil
}

// countGuests tallies running vs stopped guests from a /qemu or /lxc listing,
// ignoring permission/transport errors (the inventory is best-effort per node).
func countGuests(ctx context.Context, cfg map[string]string, path string, inv *pveInventory) {
	var res struct {
		Data []struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := pveGet(ctx, cfg, path, &res); err != nil {
		return
	}
	for _, g := range res.Data {
		inv.Total++
		if g.Status == "running" {
			inv.Running++
		} else {
			inv.Stopped++
		}
	}
}

// pveDashboard is the aggregated supervision snapshot returned to the UI.
type pveDashboard struct {
	Warning string `json:"warning,omitempty"` // non-fatal: e.g. node stats refused
	CPU struct {
		Usage float64 `json:"usage"` // 0..1, weighted by cores across online nodes
		Cores int     `json:"cores"`
	} `json:"cpu"`
	Memory struct {
		Used  int64 `json:"used"`
		Total int64 `json:"total"`
	} `json:"memory"`
	Storage struct {
		Used  int64 `json:"used"`
		Total int64 `json:"total"`
	} `json:"storage"`

	VMs        pveInventory  `json:"vms"`
	Containers pveInventory  `json:"containers"`
	Nodes      []pveNodeStat `json:"nodes"`
}

type pveInventory struct {
	Running int `json:"running"`
	Stopped int `json:"stopped"`
	Total   int `json:"total"`
}

type pveNodeStat struct {
	Name   string  `json:"name"`
	Status string  `json:"status"`
	CPU    float64 `json:"cpu"`
	Mem    int64   `json:"mem"`
	MaxMem int64   `json:"maxmem"`
}

type pveVM struct {
	VMID   int    `json:"vmid"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Node   string `json:"node"`
}

// pveListVMs uses /cluster/resources?type=vm: a single call that returns every
// guest with its owning node, avoiding a per-node fan-out.
func pveListVMs(ctx context.Context, cfg map[string]string) ([]pveVM, error) {
	var res struct {
		Data []pveVM `json:"data"`
	}
	if err := pveGet(ctx, cfg, "/cluster/resources?type=vm", &res); err != nil {
		return nil, err
	}
	return res.Data, nil
}

func pveNodeForVM(ctx context.Context, cfg map[string]string, vmid int) (string, error) {
	if n := cfg["node"]; n != "" {
		return n, nil
	}
	vms, err := pveListVMs(ctx, cfg)
	if err != nil {
		return "", err
	}
	for _, v := range vms {
		if v.VMID == vmid {
			return v.Node, nil
		}
	}
	return "", fmt.Errorf("VM %d not found in the cluster", vmid)
}

func pveAuth(cfg map[string]string) string {
	return fmt.Sprintf("PVEAPIToken=%s=%s", cfg["token_id"], cfg["token_secret"])
}

func pveGet(ctx context.Context, cfg map[string]string, path string, out any) error {
	return pveDo(ctx, cfg, http.MethodGet, path, out)
}

func pvePost(ctx context.Context, cfg map[string]string, path string) error {
	return pveDo(ctx, cfg, http.MethodPost, path, nil)
}

func pveDo(ctx context.Context, cfg map[string]string, method, path string, out any) error {
	base := strings.TrimRight(cfg["base_url"], "/")
	req, err := http.NewRequestWithContext(ctx, method, base+"/api2/json"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", pveAuth(cfg))
	resp, err := httpClient(cfg["verify_tls"] != "true").Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("proxmox %s: %s", path, resp.Status)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
