package cache

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/adrg/xdg"
	"github.com/golang/glog"

	"github.com/freemyipod/wInd3x/pkg/app"
	"github.com/freemyipod/wInd3x/pkg/devices"
	"github.com/freemyipod/wInd3x/pkg/exploit/decrypt"
	"github.com/freemyipod/wInd3x/pkg/image"
)

type PayloadKind string

const (
	PayloadKindWTFUpstream       PayloadKind = "wtf-upstream"
	PayloadKindWTFDecrypted      PayloadKind = "wtf-decrypted"
	PayloadKindWTFDecryptedCache PayloadKind = "wtf-decrypted-cache"
	PayloadKindWTFDefanged       PayloadKind = "wtf-defanged"

	PayloadKindRecoveryUpstream PayloadKind = "recovery-upstream"

	PayloadKindFirmwareUpstream   PayloadKind = "firmware-upstream"
	PayloadKindBootloaderUpstream PayloadKind = "bootloader-upstream"

	PayloadKindJingleXML PayloadKind = "jinglexml"
)

func getPayloadFromPhobosIPSW(pk PayloadKind, dk devices.Kind) error {
	var url string
	var err error
	switch pk {
	case PayloadKindWTFUpstream:
		url, err = RecoveryWTFURL(dk)
	case PayloadKindRecoveryUpstream:
		url, err = RecoveryFirmwareDFUURL(dk)
	case PayloadKindFirmwareUpstream, PayloadKindBootloaderUpstream:
		url, err = FirmwareURL(dk)
	default:
		err = fmt.Errorf("don't know how to get a %s", pk)
	}
	if err != nil {
		return err
	}

	glog.Infof("Downloading %s IPSW from %s...", pk, url)
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("could not download IPSW: %w", err)
	}
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("could not download IPSW: %w", err)
	}
	z, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return fmt.Errorf("could not parse IPSW: %w", err)
	}

	var want *regexp.Regexp
	switch pk {
	case PayloadKindWTFUpstream:
		want = regexp.MustCompile(`^firmware/dfu/wtf.*release\.dfu$`)
	case PayloadKindRecoveryUpstream:
		want = regexp.MustCompile(`^firmware/dfu/firmware.*release\.dfu$`)
	case PayloadKindFirmwareUpstream:
		want = regexp.MustCompile(`^firmware.*$`)
	case PayloadKindBootloaderUpstream:
		want = regexp.MustCompile(`^n.*\.bootloader.*\.rb3$`)
	default:
		return fmt.Errorf("don't know file path for %s", pk)
	}
	var fname string
	for _, f := range z.File {
		if want.MatchString(strings.ToLower(f.Name)) {
			fname = f.Name
		}
	}
	if fname == "" {
		return fmt.Errorf("expected file not found in IPSW")
	}
	f, err := z.Open(fname)
	if err != nil {
		return fmt.Errorf("could not open %q in IPSW: %w", fname, err)
	}
	data, err := ioutil.ReadAll(f)
	if err != nil {
		return fmt.Errorf("could not read %q from IPSW: %w", fname, err)
	}

	fspath := pathFor(&dk, pk)
	os.MkdirAll(filepath.Dir(fspath), 0755)
	if err := os.WriteFile(fspath, data, 0644); err != nil {
		return fmt.Errorf("could not write: %w", err)
	}
	return nil
}

func getWTFDecrypted(app *app.App) error {
	encrypted, err := Get(app, PayloadKindWTFUpstream)
	if err != nil {
		return err
	}
	img1, err := image.Read(bytes.NewReader(encrypted))
	if err != nil {
		return fmt.Errorf("could not parse WTF IMG1: %w", err)
	}

	recovery := pathFor(&app.Desc.Kind, PayloadKindWTFDecryptedCache)
	decrypted, err := decrypt.Decrypt(app, img1.Body, recovery)
	if err != nil {
		return fmt.Errorf("could not decrypt WTF: %w", err)
	}

	wrapper, err := image.MakeUnsigned(app.Desc.Kind, img1.Header.Entrypoint, decrypted)
	if err != nil {
		return fmt.Errorf("could not re-pack decrypted WTF: %w", err)
	}

	fspath := pathFor(&app.Desc.Kind, PayloadKindWTFDecrypted)
	os.MkdirAll(filepath.Dir(fspath), 0755)
	if err := os.WriteFile(fspath, wrapper, 0644); err != nil {
		return fmt.Errorf("could not write WTF: %w", err)
	}
	os.Remove(recovery)
	return nil
}

func getWTFDefanged(app *app.App) error {
	defanger, ok := wtfDefangers[app.Desc.Kind]
	if !ok {
		return fmt.Errorf("don't know how to defang a %s", app.Desc.Kind)
	}

	decrypted, err := Get(app, PayloadKindWTFDecrypted)
	if err != nil {
		return err
	}
	defanged, err := defanger(decrypted)
	if err != nil {
		return fmt.Errorf("defanging failed: %w", err)
	}

	fspath := pathFor(&app.Desc.Kind, PayloadKindWTFDefanged)
	os.MkdirAll(filepath.Dir(fspath), 0755)
	if err := os.WriteFile(fspath, defanged, 0644); err != nil {
		return fmt.Errorf("could not write WTF: %w", err)
	}
	return nil
}

func Get(app *app.App, payload PayloadKind) ([]byte, error) {
	fspath := pathFor(&app.Desc.Kind, payload)
	if _, err := os.Stat(fspath); err == nil {
		glog.Infof("Using %s %s at %s", app.Desc.Kind, payload, fspath)
		return os.ReadFile(fspath)
	}

	var err error
	switch payload {
	case PayloadKindWTFUpstream, PayloadKindRecoveryUpstream, PayloadKindFirmwareUpstream, PayloadKindBootloaderUpstream:
		err = getPayloadFromPhobosIPSW(payload, app.Desc.Kind)
	case PayloadKindWTFDecrypted:
		err = getWTFDecrypted(app)
	case PayloadKindWTFDefanged:
		err = getWTFDefanged(app)
	default:
		return nil, fmt.Errorf("don't know how to get a %s", payload)
	}
	if err != nil {
		return nil, err
	}

	return os.ReadFile(fspath)
}

func pathFor(dev *devices.Kind, payload PayloadKind) string {
	devpart := "any"
	if dev != nil {
		devpart = string(*dev)
	}
	parts := []string{
		xdg.DataHome,
		"wInd3x",
		fmt.Sprintf("%s-%s.bin", devpart, payload),
	}
	return path.Join(parts...)
}
