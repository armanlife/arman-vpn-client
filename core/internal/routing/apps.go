package routing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/armanlife/arman-vpn-client/core/internal/securefile"
)

// AppListURLs are where a region's phone apps that should connect directly
// come from by default, tried in order ({region} is its code):
// arman-org/android-direct-apps (kept by hand there, each app with its
// names, category, and stores), from GitHub, then through jsDelivr. The
// list is JSON, only the
// package names: {country, apps: [package, ...]}. The lists are GPL-3.0,
// so they are downloaded, not shipped.
var AppListURLs = []string{
	"https://raw.githubusercontent.com/arman-org/android-direct-apps/main/dist/{region}.json",
	"https://cdn.jsdelivr.net/gh/arman-org/android-direct-apps@main/dist/{region}.json",
}

// appListURLs are AppListURLs for region.
func appListURLs(region string) []string {
	out := make([]string, len(AppListURLs))
	for i, u := range AppListURLs {
		out[i] = strings.ReplaceAll(u, "{region}", region)
	}
	return out
}

// appListRegions are the regions that have a list.
var appListRegions = map[string]bool{RegionIran: true, "ru": true}

// maxAppList bounds a downloaded app list.
const maxAppList = 1 << 20

var packageName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z0-9_]+)+$`)

// HasAppList reports whether region has a list of apps.
func HasAppList(region string) bool { return appListRegions[region] }

// AppListFile is the name of region's app list in the routing folder.
func AppListFile(region string) string { return "apps-direct-" + region + ".json" }

// AppListDue reports whether region has a list that is missing from dir
// or older than maxAge.
func AppListDue(dir, region string, maxAge time.Duration) bool {
	if !HasAppList(region) {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, AppListFile(region)))
	return err != nil || time.Since(info.ModTime()) >= maxAge
}

// DownloadAppList fetches region's app list with client into dir, from the
// first of links' addresses for it that answers with one. The file is
// replaced only by a complete list of package names.
func DownloadAppList(ctx context.Context, client *http.Client, dir, region string) error {
	if !HasAppList(region) {
		return nil
	}
	if err := securefile.EnsurePrivateDir(dir); err != nil {
		return err
	}
	var errs []error
	for _, url := range appListURLs(region) {
		data, err := fetchAppList(ctx, client, url)
		if err == nil {
			return securefile.WritePrivate(filepath.Join(dir, AppListFile(region)), data)
		}
		errs = append(errs, err)
		if ctx.Err() != nil {
			break
		}
	}
	return errors.Join(errs...)
}

// fetchAppList downloads one list and returns it when it holds package
// names.
func fetchAppList(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAppList+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxAppList {
		return nil, fmt.Errorf("app list larger than %d KiB from %s", maxAppList>>10, url)
	}
	if len(parseAppList(data)) == 0 {
		return nil, fmt.Errorf("no package names in %s", url)
	}
	return data, nil
}

// ReadAppList returns the package names of region's list in dir; none
// when it is missing.
func ReadAppList(dir, region string) []string {
	if !HasAppList(region) {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(dir, AppListFile(region)))
	if err != nil {
		return nil
	}
	return parseAppList(data)
}

// parseAppList returns the list's package names; none when it is not a
// list. An entry whose package is not a package name is left out.
func parseAppList(data []byte) []string {
	var list struct {
		Apps []string `json:"apps"`
	}
	if json.Unmarshal(data, &list) != nil {
		return nil
	}
	var apps []string
	for _, app := range list.Apps {
		if packageName.MatchString(app) {
			apps = append(apps, app)
		}
	}
	return apps
}
