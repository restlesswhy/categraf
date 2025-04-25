package redfish

import (
	"crypto/tls"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"flashcat.cloud/categraf/config"
	"flashcat.cloud/categraf/inputs"
	"flashcat.cloud/categraf/types"
	"github.com/tidwall/gjson"
)

const (
	inputName = "redfish"
)

type Redfish struct {
	config.PluginConfig
	Instances []*Instance `toml:"instances"`
}

func init() {
	inputs.Add(inputName, func() inputs.Input {
		return &Redfish{}
	})
}

func (r *Redfish) Clone() inputs.Input {
	return &Redfish{}
}

func (r *Redfish) Name() string {
	return inputName
}

func (r *Redfish) GetInstances() []inputs.Instance {
	out := make([]inputs.Instance, len(r.Instances))
	for i := 0; i < len(r.Instances); i++ {
		out[i] = r.Instances[i]
	}
	return out
}

type Instance struct {
	Addresses []Address `toml:"addresses"`
	Sets      []Set     `toml:"sets"`

	config.InstanceConfig
}

type Address struct {
	config.HTTPCommonConfig

	URL      string        `toml:"url"`
	Username config.Secret `toml:"username"`
	Password config.Secret `toml:"password"`

	client  *http.Client
	baseURL *url.URL
}

type Set struct {
	URN     string   `toml:"urn"`
	Prefix  string   `toml:"prefix"`
	Metrics []Metric `toml:"metrics"`
}

type Metric struct {
	Name   string `toml:"name"`
	Prefix string `toml:"prefix"`
	Path   string `toml:"path"`
}

func (i *Instance) Init() error {
	for idx, v := range i.Addresses {
		if v.URL == "" {
			return errors.New("did not provide IP")
		}

		base, err := url.Parse(v.URL)
		if err != nil {
			return fmt.Errorf("parse URL: %w", err)
		}

		i.Addresses[idx].baseURL = base
		i.Addresses[idx].InitHTTPClientConfig()
		i.Addresses[idx].client = &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true,
				},
			},
			Timeout: time.Duration(v.Timeout),
		}
	}

	if len(i.Sets) == 0 {
		return errors.New("metrics are not requested")
	}

	return nil
}

func join(in ...string) string {
	var parts []string
	for _, s := range in {
		if strings.TrimSpace(s) != "" {
			parts = append(parts, s)
		}
	}

	return strings.Join(parts, "_")
}

func (i *Instance) Gather(sList *types.SampleList) {
	for _, a := range i.Addresses {
		for _, s := range i.Sets {
			ready := a.baseURL.ResolveReference(&url.URL{Path: s.URN})

			js, err := a.getData(ready.String())
			if err != nil {
				log.Println("E! error getData", err)
				continue
			}

			tags := map[string]string{
				"address": a.baseURL.Host,
			}

			fields := make(map[string]interface{})
			for _, m := range s.Metrics {
				value := gjson.Get(js, m.Path)

				for i, v := range value.Array() {
					fields[join(m.Prefix, m.Name, strconv.Itoa(i))] = v.Value()
				}
			}

			sList.PushSamples(s.Prefix, fields, tags)
		}
	}
}

func (i *Address) getData(uri string) (string, error) {
	req, err := http.NewRequest(i.Method, uri, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	username, err := i.Username.Get()
	if err != nil {
		return "", fmt.Errorf("getting username failed: %w", err)
	}
	user := username.String()
	username.Destroy()

	password, err := i.Password.Get()
	if err != nil {
		return "", fmt.Errorf("getting password failed: %w", err)
	}
	pass := password.String()
	password.Destroy()

	req.SetBasicAuth(user, pass)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := i.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("client do: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("received status code %d (%s) for address %s, expected 200",
			resp.StatusCode,
			http.StatusText(resp.StatusCode),
			uri)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read all: %w", err)
	}

	return string(body), nil
}
