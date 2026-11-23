// Package packer builds the blob directory that internal/embedtools embeds: each verified
// executable compressed with zstd, the license texts, the notices and manifest.json. It runs at
// build time only (scripts/embed-tools.sh) and is not linked into abacus.
package packer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/andrew-bierman/abacus/internal/embedtools"
	"github.com/klauspost/compress/zstd"
)

// Tool is an input executable: Src/<Name> is the verified file.
type Tool struct{ Name, Version string }

// Input says what to pack. Src holds one regular file per tool (named Name); Licenses holds
// <name>/LICENSE for tools and linked libraries; Notices is THIRD_PARTY_NOTICES.md.
type Input struct {
	Src, Licenses, Notices, Out string
	Tools                       []Tool
}

func readRegular(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	return os.ReadFile(path)
}

// Pack writes the blob directory into in.Out, which must not exist or be empty. It fails closed:
// on any error nothing usable is left behind.
func Pack(in Input) (err error) {
	if es, e := os.ReadDir(in.Out); e == nil && len(es) > 0 {
		return fmt.Errorf("%s is not empty", in.Out)
	}
	if err := os.MkdirAll(in.Out, 0o755); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			os.RemoveAll(in.Out)
		}
	}()
	var m embedtools.Manifest
	m.Schema = 1
	for _, t := range in.Tools {
		src, e := os.Open(filepath.Join(in.Src, t.Name))
		if e != nil {
			return e
		}
		fi, e := src.Stat()
		if e != nil || !fi.Mode().IsRegular() {
			src.Close()
			return fmt.Errorf("%s is not a regular file", t.Name)
		}
		file := t.Name + ".zst"
		out, e := os.OpenFile(filepath.Join(in.Out, file), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if e != nil {
			src.Close()
			return e
		}
		enc, e := zstd.NewWriter(out, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
		if e != nil {
			src.Close()
			out.Close()
			return e
		}
		h := sha256.New()
		n, e := io.Copy(enc, io.TeeReader(src, h))
		src.Close()
		if e == nil {
			e = enc.Close()
		}
		if ce := out.Close(); e == nil {
			e = ce
		}
		if e != nil {
			return fmt.Errorf("%s: %w", t.Name, e)
		}
		et := embedtools.Tool{Name: t.Name, Version: t.Version, File: file, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}
		m.Tools = append(m.Tools, et)
	}
	entries, _ := os.ReadDir(in.Licenses)
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names {
		files, e := os.ReadDir(filepath.Join(in.Licenses, n))
		if e != nil {
			return e
		}
		if e := os.MkdirAll(filepath.Join(in.Out, "licenses", n), 0o755); e != nil {
			return e
		}
		for _, f := range files { // LICENSE, and any further texts the tool's build collected
			b, e := readRegular(filepath.Join(in.Licenses, n, f.Name()))
			if e != nil {
				return e
			}
			rel := "licenses/" + n + "/" + f.Name()
			if e := os.WriteFile(filepath.Join(in.Out, rel), b, 0o644); e != nil {
				return e
			}
			isLicense := false
			for k := range m.Tools {
				if m.Tools[k].Name == n && f.Name() == "LICENSE" {
					m.Tools[k].LicenseFile, isLicense = rel, true
				}
			}
			if !isLicense {
				m.Licenses = append(m.Licenses, rel)
			}
		}
	}
	nb, e := readRegular(in.Notices)
	if e != nil {
		return e
	}
	if e := os.WriteFile(filepath.Join(in.Out, embedtools.NoticesFile), nb, 0o644); e != nil {
		return e
	}
	mb, _ := json.MarshalIndent(m, "", "  ")
	if _, e := embedtools.Parse(mb); e != nil { // what we write must be what the binary accepts
		return e
	}
	return os.WriteFile(filepath.Join(in.Out, embedtools.ManifestFile), mb, 0o644)
}
