package hodu

import "fmt"
import "os"
import "path/filepath"
import "sort"
import "strings"
import "time"

import yaml "github.com/goccy/go-yaml"

const CLIENT_RXC_PROFILE_RELOAD_MIN_INTERVAL time.Duration = 5 * time.Second

type ClientRxcProfile struct {
	Name   string `yaml:"name"`
	Script string `yaml:"script"`
	User   string `yaml:"user"`
}

type client_rxc_profile_file_doc struct {
	Profiles []ClientRxcProfile `yaml:"profiles"`
}

type client_rxc_profile_file_state struct {
	mod_time time.Time
	size     int64
}

type ClientRxcProfileMap map[string]*ClientRxcProfile

func (c *Client) SetRxcProfileFiles(files []string) {
	var copied []string

	copied = make([]string, len(files))
	copy(copied, files)

	c.rxc_profile_mtx.Lock()
	c.rxc_profile_files = copied
	c.rxc_profile_map = make(ClientRxcProfileMap)
	c.rxc_profile_file_states = make(map[string]client_rxc_profile_file_state)
	c.rxc_profile_last_check = time.Time{}
	c.rxc_profile_loaded = false
	c.rxc_profile_mtx.Unlock()
}

func (c *Client) GetRxcProfileFiles() []string {
	var copied []string

	c.rxc_profile_mtx.Lock()
	copied = make([]string, len(c.rxc_profile_files))
	copy(copied, c.rxc_profile_files)
	c.rxc_profile_mtx.Unlock()

	return copied
}

func append_client_rxc_profiles(dst ClientRxcProfileMap, src []ClientRxcProfile, source_file string) error {
	var profile ClientRxcProfile
	var copied ClientRxcProfile
	var existing *ClientRxcProfile
	var ok bool

	for _, profile = range src {
		profile.Name = strings.TrimSpace(profile.Name)
		profile.Script = strings.TrimSpace(profile.Script)
		profile.User = strings.TrimSpace(profile.User)

		if profile.Name == "" {
			return fmt.Errorf("blank rxc profile name in %s", source_file)
		}
		if profile.Script == "" {
			return fmt.Errorf("blank rxc profile script for %s in %s", profile.Name, source_file)
		}

		existing, ok = dst[profile.Name]
		if ok && existing != nil {
			return fmt.Errorf("duplicate rxc profile %s in %s", profile.Name, source_file)
		}

		copied = profile
		dst[copied.Name] = &copied
	}

	return nil
}

func (c *Client) reload_rxc_profiles_if_needed() error {
	var now time.Time
	var min_interval time.Duration
	var patterns []string
	var matched_file_map map[string]struct{}
	var matched_files []string
	var pattern string
	var file_path string
	var file *os.File
	var file_states map[string]client_rxc_profile_file_state
	var profiles ClientRxcProfileMap
	var changed bool
	var current_state client_rxc_profile_file_state
	var doc client_rxc_profile_file_doc
	var dec *yaml.Decoder
	var err error

	now = time.Now()

	c.rxc_profile_mtx.Lock()
	defer c.rxc_profile_mtx.Unlock()

	min_interval = c.rxc_profile_reload_min_interval

	if min_interval > 0 && !c.rxc_profile_last_check.IsZero() && now.Before(c.rxc_profile_last_check.Add(min_interval)) {
		return nil
	}
	c.rxc_profile_last_check = now

	patterns = make([]string, len(c.rxc_profile_files))
	copy(patterns, c.rxc_profile_files)

	matched_file_map = make(map[string]struct{})
	for _, pattern = range patterns {
		var expanded []string

		if strings.TrimSpace(pattern) == "" { continue }

		expanded, err = filepath.Glob(pattern)
		if err != nil { return fmt.Errorf("invalid rxc profile file pattern %s - %s", pattern, err.Error()) }

		for _, file_path = range expanded {
			matched_file_map[file_path] = struct{}{}
		}
	}

	matched_files = make([]string, 0, len(matched_file_map))
	for file_path = range matched_file_map {
		matched_files = append(matched_files, file_path)
	}
	sort.Strings(matched_files)

	file_states = make(map[string]client_rxc_profile_file_state)
	for _, file_path = range matched_files {
		var file_info os.FileInfo

		file_info, err = os.Stat(file_path)
		if err != nil {
			return fmt.Errorf("failed to stat rxc profile file %s - %s", file_path, err.Error())
		}

		file_states[file_path] = client_rxc_profile_file_state{
			mod_time: file_info.ModTime(),
			size:     file_info.Size(),
		}
	}

	if len(file_states) != len(c.rxc_profile_file_states) {
		changed = true
	} else {
		var ok bool
		for file_path, current_state = range file_states {
			_, ok = c.rxc_profile_file_states[file_path]
			if !ok {
				changed = true
				break
			}
			if !c.rxc_profile_file_states[file_path].mod_time.Equal(current_state.mod_time) || c.rxc_profile_file_states[file_path].size != current_state.size {
				changed = true
				break
			}
		}
	}

	if !changed && c.rxc_profile_loaded { return nil }

	profiles = make(ClientRxcProfileMap)
	for _, file_path = range matched_files {
		file, err = os.Open(file_path)
		if err != nil { return fmt.Errorf("failed to open rxc profile file %s - %s", file_path, err.Error()) }

		doc = client_rxc_profile_file_doc{}
		dec = yaml.NewDecoder(file)
		err = dec.Decode(&doc)
		file.Close()
		if err != nil { return fmt.Errorf("failed to parse rxc profile file %s - %s", file_path, err.Error()) }

		err = append_client_rxc_profiles(profiles, doc.Profiles, file_path)
		if err != nil { return err }
	}

	c.rxc_profile_map = profiles
	c.rxc_profile_file_states = file_states
	c.rxc_profile_loaded = true
	c.log.Write("", LOG_DEBUG, "Loaded %d rxc profiles from %d files", len(c.rxc_profile_map), len(matched_files))

	return nil
}

func (c *Client) ResolveRxcProfile(name string) (*ClientRxcProfile, error) {
	var profile *ClientRxcProfile
	var copied ClientRxcProfile
	var ok bool
	var err error

	name = strings.TrimSpace(name)
	if name == "" { return nil, fmt.Errorf("blank rxc profile name") }

	err = c.reload_rxc_profiles_if_needed()

	c.rxc_profile_mtx.Lock()
	profile, ok = c.rxc_profile_map[name]
	if ok && profile != nil { copied = *profile }
	c.rxc_profile_mtx.Unlock()

	if ok && profile != nil { return &copied, nil }
	if err != nil { return nil, err } // reloading fails and there is no existing profile
	return nil, nil
}
