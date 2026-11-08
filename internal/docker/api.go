package docker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

func esc(s string) string { return url.PathEscape(s) }

// Ping checks connectivity.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.getRaw(ctx, "/_ping", nil)
	return err
}

// Info returns daemon info.
func (c *Client) Info(ctx context.Context) (*Info, error) {
	var i Info
	return &i, c.getJSON(ctx, "/info", nil, &i)
}

// Version returns daemon version info.
func (c *Client) Version(ctx context.Context) (*Version, error) {
	var v Version
	return &v, c.getJSON(ctx, "/version", nil, &v)
}

// DiskUsage returns /system/df. typ may be "" or one of container, image,
// volume, build-cache.
func (c *Client) DiskUsage(ctx context.Context, typ string) (*DiskUsage, error) {
	var q url.Values
	if typ != "" {
		q = url.Values{"type": {typ}}
	}
	var d DiskUsage
	return &d, c.getJSON(ctx, "/system/df", q, &d)
}

// ---- containers

// ContainerList lists containers.
func (c *Client) ContainerList(ctx context.Context, all bool) ([]Container, error) {
	q := url.Values{}
	if all {
		q.Set("all", "1")
	}
	var out []Container
	return out, c.getJSON(ctx, "/containers/json", q, &out)
}

// ContainerInspect returns the parsed and raw inspect output.
func (c *Client) ContainerInspect(ctx context.Context, id string) (*ContainerJSON, []byte, error) {
	raw, err := c.getRaw(ctx, "/containers/"+esc(id)+"/json", nil)
	if err != nil {
		return nil, nil, err
	}
	var cj ContainerJSON
	if err := json.Unmarshal(raw, &cj); err != nil {
		return nil, raw, err
	}
	return &cj, raw, nil
}

// ContainerStats returns a single stats sample (one-shot, no precpu).
func (c *Client) ContainerStats(ctx context.Context, id string) (*Stats, error) {
	var s Stats
	q := url.Values{"stream": {"false"}, "one-shot": {"true"}}
	return &s, c.getJSON(ctx, "/containers/"+esc(id)+"/stats", q, &s)
}

func (c *Client) ContainerStart(ctx context.Context, id string) error {
	return c.post(ctx, "/containers/"+esc(id)+"/start", nil, nil, nil)
}

func (c *Client) ContainerStop(ctx context.Context, id string) error {
	return c.post(ctx, "/containers/"+esc(id)+"/stop", nil, nil, nil)
}

func (c *Client) ContainerRestart(ctx context.Context, id string) error {
	return c.post(ctx, "/containers/"+esc(id)+"/restart", nil, nil, nil)
}

func (c *Client) ContainerPause(ctx context.Context, id string) error {
	return c.post(ctx, "/containers/"+esc(id)+"/pause", nil, nil, nil)
}

func (c *Client) ContainerUnpause(ctx context.Context, id string) error {
	return c.post(ctx, "/containers/"+esc(id)+"/unpause", nil, nil, nil)
}

func (c *Client) ContainerKill(ctx context.Context, id string) error {
	return c.post(ctx, "/containers/"+esc(id)+"/kill", nil, nil, nil)
}

// ContainerRemove removes a container (force kills it if running).
func (c *Client) ContainerRemove(ctx context.Context, id string, force, volumes bool) error {
	q := url.Values{"force": {strconv.FormatBool(force)}, "v": {strconv.FormatBool(volumes)}}
	return c.delete(ctx, "/containers/"+esc(id), q)
}

// LogOptions controls ContainerLogs.
type LogOptions struct {
	Tail       string
	Follow     bool
	Timestamps bool
}

// ContainerLogs returns the raw log stream (multiplexed unless the container has a TTY).
func (c *Client) ContainerLogs(ctx context.Context, id string, o LogOptions) (io.ReadCloser, error) {
	q := url.Values{
		"stdout":     {"1"},
		"stderr":     {"1"},
		"follow":     {strconv.FormatBool(o.Follow)},
		"timestamps": {strconv.FormatBool(o.Timestamps)},
	}
	if o.Tail != "" {
		q.Set("tail", o.Tail)
	}
	resp, err := c.do(ctx, http.MethodGet, "/containers/"+esc(id)+"/logs", q, nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// ContainerAttach attaches to a container's stdio.
func (c *Client) ContainerAttach(ctx context.Context, id string) (*HijackedConn, error) {
	q := url.Values{
		"stream": {"1"}, "stdin": {"1"}, "stdout": {"1"}, "stderr": {"1"},
		"detachKeys": {"ctrl-p,ctrl-q"},
	}
	return c.hijack(ctx, "/containers/"+esc(id)+"/attach", q, nil)
}

// ContainerResize resizes a container's TTY.
func (c *Client) ContainerResize(ctx context.Context, id string, h, w uint) error {
	q := url.Values{"h": {strconv.Itoa(int(h))}, "w": {strconv.Itoa(int(w))}}
	return c.post(ctx, "/containers/"+esc(id)+"/resize", q, nil, nil)
}

// ContainerPrune removes stopped containers.
func (c *Client) ContainerPrune(ctx context.Context) (int, uint64, error) {
	var r struct {
		ContainersDeleted []string `json:"ContainersDeleted"`
		SpaceReclaimed    uint64   `json:"SpaceReclaimed"`
	}
	err := c.post(ctx, "/containers/prune", nil, nil, &r)
	return len(r.ContainersDeleted), r.SpaceReclaimed, err
}

// ---- exec

// ExecCreate creates an exec instance and returns its ID.
func (c *Client) ExecCreate(ctx context.Context, id string, cmd, env []string, tty bool) (string, error) {
	body := map[string]any{
		"AttachStdin": true, "AttachStdout": true, "AttachStderr": true,
		"Tty": tty, "Cmd": cmd, "Env": env,
	}
	var r struct {
		ID string `json:"Id"`
	}
	err := c.post(ctx, "/containers/"+esc(id)+"/exec", nil, body, &r)
	return r.ID, err
}

// ExecStart starts an exec instance and returns the hijacked stream.
func (c *Client) ExecStart(ctx context.Context, execID string, tty bool, h, w uint) (*HijackedConn, error) {
	body := map[string]any{"Detach": false, "Tty": tty}
	if h > 0 && w > 0 {
		body["ConsoleSize"] = []uint{h, w}
	}
	return c.hijack(ctx, "/exec/"+esc(execID)+"/start", nil, body)
}

// ExecResize resizes an exec TTY.
func (c *Client) ExecResize(ctx context.Context, execID string, h, w uint) error {
	q := url.Values{"h": {strconv.Itoa(int(h))}, "w": {strconv.Itoa(int(w))}}
	return c.post(ctx, "/exec/"+esc(execID)+"/resize", q, nil, nil)
}

// ExecExitCode returns the exit code of a finished exec.
func (c *Client) ExecExitCode(ctx context.Context, execID string) (int, bool, error) {
	var r struct {
		Running  bool `json:"Running"`
		ExitCode int  `json:"ExitCode"`
	}
	err := c.getJSON(ctx, "/exec/"+esc(execID)+"/json", nil, &r)
	return r.ExitCode, r.Running, err
}

// ---- images

func (c *Client) ImageList(ctx context.Context) ([]Image, error) {
	var out []Image
	return out, c.getJSON(ctx, "/images/json", nil, &out)
}

func (c *Client) ImageInspectRaw(ctx context.Context, id string) ([]byte, error) {
	return c.getRaw(ctx, "/images/"+esc(id)+"/json", nil)
}

func (c *Client) ImageRemove(ctx context.Context, id string, force bool) error {
	return c.delete(ctx, "/images/"+esc(id), url.Values{"force": {strconv.FormatBool(force)}})
}

// ImagePrune removes dangling images (or all unused if all is set).
func (c *Client) ImagePrune(ctx context.Context, all bool) (int, uint64, error) {
	var q url.Values
	if all {
		q = url.Values{"filters": {`{"dangling":["false"]}`}}
	}
	var r struct {
		ImagesDeleted  []map[string]string `json:"ImagesDeleted"`
		SpaceReclaimed uint64              `json:"SpaceReclaimed"`
	}
	err := c.post(ctx, "/images/prune", q, nil, &r)
	return len(r.ImagesDeleted), r.SpaceReclaimed, err
}

// ---- networks

func (c *Client) NetworkList(ctx context.Context) ([]Network, error) {
	var out []Network
	return out, c.getJSON(ctx, "/networks", nil, &out)
}

func (c *Client) NetworkInspectRaw(ctx context.Context, id string) ([]byte, error) {
	return c.getRaw(ctx, "/networks/"+esc(id), url.Values{"verbose": {"true"}})
}

func (c *Client) NetworkRemove(ctx context.Context, id string) error {
	return c.delete(ctx, "/networks/"+esc(id), nil)
}

func (c *Client) NetworkPrune(ctx context.Context) (int, error) {
	var r struct {
		NetworksDeleted []string `json:"NetworksDeleted"`
	}
	err := c.post(ctx, "/networks/prune", nil, nil, &r)
	return len(r.NetworksDeleted), err
}

// ---- volumes

func (c *Client) VolumeList(ctx context.Context) ([]Volume, error) {
	var out struct {
		Volumes []Volume `json:"Volumes"`
	}
	return out.Volumes, c.getJSON(ctx, "/volumes", nil, &out)
}

func (c *Client) VolumeInspectRaw(ctx context.Context, name string) ([]byte, error) {
	return c.getRaw(ctx, "/volumes/"+esc(name), nil)
}

func (c *Client) VolumeRemove(ctx context.Context, name string, force bool) error {
	return c.delete(ctx, "/volumes/"+esc(name), url.Values{"force": {strconv.FormatBool(force)}})
}

// VolumePrune removes unused anonymous volumes (all unused when all is set).
func (c *Client) VolumePrune(ctx context.Context, all bool) (int, uint64, error) {
	var q url.Values
	if all {
		q = url.Values{"filters": {`{"all":["true"]}`}}
	}
	var r struct {
		VolumesDeleted []string `json:"VolumesDeleted"`
		SpaceReclaimed uint64   `json:"SpaceReclaimed"`
	}
	err := c.post(ctx, "/volumes/prune", q, nil, &r)
	return len(r.VolumesDeleted), r.SpaceReclaimed, err
}
