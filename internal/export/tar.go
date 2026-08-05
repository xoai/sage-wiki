// Package export provides the shared deterministic workspace exporter
// (SPEC-04 D5). One implementation — pkg/engine.Workspace.Export and the
// serve-mode /export endpoint both delegate here (ground rule 2).
package export

import (
	"archive/tar"
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/xoai/sage-wiki/internal/config"
)

// Tar streams a deterministic tar of dir onto dst:
//   - lexical entry order (filepath.WalkDir)
//   - ModTime zeroed, or the SOURCE_DATE_EPOCH when pinned
//   - Uid/Gid/Uname/Gname zeroed (checkout-local values must not leak
//     into artifact bytes)
//   - symlinks skipped (empty link targets break readers)
//   - .sage/engine.lock skipped (not workspace content)
//
// A live SQLite DB inside dir is copied as-is — a concurrent compile may
// leave it inconsistent (documented caveat; backup-API snapshot is out of
// scope, same as the pre-D5 implementations).
func Tar(ctx context.Context, dir string, dst io.Writer) error {
	tw := tar.NewWriter(dst)
	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == dir {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		// ToSlash: filepath.Rel returns OS separators (backslash on Windows),
		// so the exclusion must compare the normalized form.
		if filepath.ToSlash(rel) == ".sage/engine.lock" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		hdr.ModTime = exportModTime()
		hdr.Uid, hdr.Gid = 0, 0
		hdr.Uname, hdr.Gname = "", ""
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, f)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err := tw.Close(); walkErr == nil {
		walkErr = err
	}
	return walkErr
}

// exportModTime is the SOURCE_DATE_EPOCH when pinned, else the zero time —
// either way exports are byte-reproducible.
func exportModTime() time.Time {
	if s := os.Getenv("SOURCE_DATE_EPOCH"); s != "" {
		return config.NowUTC()
	}
	return time.Time{}
}
