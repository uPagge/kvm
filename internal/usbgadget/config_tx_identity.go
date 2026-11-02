package usbgadget

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sourcegraph/tf-dag/dag"
)

func (tx *UsbGadgetTransaction) writeConfigFile(name string, data []byte) error {
	if tx.writeFile != nil {
		return tx.writeFile(name, data)
	}
	return os.WriteFile(name, data, 0644)
}

type configSnapshot struct {
	name string
	info os.FileInfo
	data []byte
}

// Reads only the attachment state needed to safely compensate configfs writes.
type configAttachmentReader struct {
	readFile func(string) ([]byte, error)
	stat     func(string) (os.FileInfo, error)
}

func (tx *UsbGadgetTransaction) attachmentStat(name string) (os.FileInfo, error) {
	if tx.attachmentReader != nil && tx.attachmentReader.stat != nil {
		return tx.attachmentReader.stat(name)
	}
	return os.Stat(name)
}

func (tx *UsbGadgetTransaction) readGadgetUDC() ([]byte, error) {
	read := os.ReadFile
	if tx.attachmentReader != nil && tx.attachmentReader.readFile != nil {
		read = tx.attachmentReader.readFile
	}
	data, err := read(filepath.Join(tx.kvmGadgetPath, "UDC"))
	if !os.IsNotExist(err) {
		return data, err
	}
	// A missing UDC is expected only before the gadget itself is created.
	// An existing configfs gadget always has this kernel-provided attribute.
	if _, statErr := tx.attachmentStat(tx.kvmGadgetPath); os.IsNotExist(statErr) {
		return nil, nil
	} else if statErr != nil {
		return nil, statErr
	}
	return nil, fmt.Errorf("read existing gadget UDC: %w", err)
}

func (tx *UsbGadgetTransaction) clearGadgetUDC() error {
	attached, err := tx.readGadgetUDC()
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(attached)) == "" {
		return nil
	}
	return tx.writeConfigFile(filepath.Join(tx.kvmGadgetPath, "UDC"), []byte("\n"))
}

func (tx *UsbGadgetTransaction) isControllerAttached() (bool, error) {
	_, err := tx.attachmentStat(filepath.Join(tx.dwc3Path, tx.udc))
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

func snapshotConfigPath(name string) (configSnapshot, error) {
	s := configSnapshot{name: name}
	info, err := os.Lstat(name)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	s.info = info
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(name)
		s.data = []byte(target)
		return s, err
	}
	if info.Mode().IsRegular() {
		s.data, err = os.ReadFile(name)
	}
	return s, err
}

// Configfs link iteration is newest first; preserve the original function
// creation order for compensation, just as checkIfSymlinksInOrder does.
func snapshotConfigLinks(dir string) ([]symlink, error) {
	if dir == "" {
		return nil, nil
	}
	f, err := os.Open(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	entries, err := f.ReadDir(-1)
	f.Close()
	if err != nil {
		return nil, err
	}
	slices.Reverse(entries)
	var links []symlink
	for _, e := range entries {
		if e.Type()&os.ModeSymlink == 0 {
			continue
		}
		name := filepath.Join(dir, e.Name())
		target, err := os.Readlink(name)
		if err != nil {
			return nil, err
		}
		links = append(links, symlink{Path: name, Target: target})
	}
	return links, nil
}

func (tx *UsbGadgetTransaction) applyConfigChanges() (result error) {
	r := ChangeSetResolver{changeset: tx.c, g: &dag.AcyclicGraph{}, l: tx.log}
	changes, err := r.GetChanges()
	if err != nil {
		return err
	}
	speedPath := filepath.Join(tx.kvmGadgetPath, "max_speed")
	hasSpeed := false
	for _, c := range changes {
		if c.Path == speedPath {
			hasSpeed = true
		}
	}
	// Transactions without speed retain the existing generic behavior.
	if !hasSpeed {
		return tx.c.Apply()
	}
	// Commit also runs after adoption of a matching live gadget. Preserve that
	// no-op; explicit platform FileWrite changes still force a rebind.
	pending := false
	for _, change := range changes {
		action := change.Action()
		if action != FileChangeResolvedActionDoNothing && !(change.IgnoreErrors && action == FileChangeResolvedActionCreateFile) {
			pending = true
			break
		}
	}
	if !pending {
		return nil
	}
	udcPath := filepath.Join(tx.kvmGadgetPath, "UDC")
	oldUDC, err := tx.readGadgetUDC()
	if err != nil {
		return err
	}
	links, err := snapshotConfigLinks(tx.configC1Path)
	if err != nil {
		return err
	}
	var snapshots []configSnapshot
	platformTouched := false
	started := false
	defer func() {
		if result == nil || !started {
			return
		}
		restoreFailed := false
		addRestoreError := func(err error) {
			if err != nil {
				restoreFailed = true
				result = errors.Join(result, err)
			}
		}
		// Never restore speed or function attributes with a bound gadget. A
		// failed write can have side effects, so compensate the attempted change.
		if err := tx.clearGadgetUDC(); err != nil {
			addRestoreError(fmt.Errorf("rollback unbind: %w", err))
			return
		}
		current, err := snapshotConfigLinks(tx.configC1Path)
		if err != nil {
			addRestoreError(err)
		}
		for _, link := range current {
			if err := os.Remove(link.Path); err != nil {
				addRestoreError(err)
			}
		}
		for i := len(snapshots) - 1; i >= 0; i-- {
			s := snapshots[i]
			var err error
			switch {
			case s.info == nil:
				err = os.Remove(s.name)
				if os.IsNotExist(err) {
					err = nil
				}
			case s.info.Mode()&os.ModeSymlink != 0:
				// Configuration links are restored together below.
				if filepath.Dir(s.name) == tx.configC1Path {
					continue
				}
				_ = os.Remove(s.name)
				err = os.Symlink(string(s.data), s.name)
			case s.info.Mode().IsRegular():
				err = tx.writeConfigFile(s.name, s.data)
			}
			if err != nil {
				addRestoreError(fmt.Errorf("rollback %s: %w", s.name, err))
			}
		}
		for _, link := range links {
			if err := os.Symlink(link.Target, link.Path); err != nil {
				addRestoreError(err)
			}
		}
		if platformTouched {
			attached, err := tx.isControllerAttached()
			if err != nil {
				addRestoreError(fmt.Errorf("rollback controller attachment: %w", err))
				return
			}
			if !attached {
				if err := tx.writeConfigFile(filepath.Join(tx.dwc3Path, "bind"), []byte(tx.udc)); err != nil {
					addRestoreError(fmt.Errorf("rollback platform bind: %w", err))
					return
				}
			}
		}
		// Do not reattach a partially restored configuration after compensation
		// itself failed. The original failure and each restore failure propagate.
		if restoreFailed {
			return
		}
		if len(strings.TrimSpace(string(oldUDC))) > 0 {
			if err := tx.writeConfigFile(udcPath, oldUDC); err != nil {
				addRestoreError(fmt.Errorf("rollback UDC: %w", err))
			}
		}
	}()
	started = true
	if strings.TrimSpace(string(oldUDC)) != "" {
		if err := tx.writeConfigFile(udcPath, []byte("\n")); err != nil {
			return err
		}
	}
	for _, change := range changes {
		change.ResetActionResolution()
		action := change.Action()
		if action == FileChangeResolvedActionDoNothing {
			continue
		}
		if strings.HasPrefix(change.Path, tx.kvmGadgetPath+string(os.PathSeparator)) && change.Path != udcPath && change.ExpectedState != FileStateSymlinkInOrderConfigFS {
			s, err := snapshotConfigPath(change.Path)
			if err != nil {
				return err
			}
			snapshots = append(snapshots, s)
		}
		if change.Path == filepath.Join(tx.dwc3Path, "unbind") {
			platformTouched = true
		}
		switch action {
		case FileChangeResolvedActionWriteFile, FileChangeResolvedActionUpdateFile, FileChangeResolvedActionCreateFile:
			err = tx.writeConfigFile(change.Path, change.ExpectedContent)
		default:
			err = tx.c.applyChange(change)
		}
		// A speed transaction must not claim success after a failed controller
		// teardown. Retain IgnoreErrors only for genuinely optional attributes.
		if err != nil && (!change.IgnoreErrors || change.Path == filepath.Join(tx.dwc3Path, "unbind")) {
			return err
		}
	}
	return nil
}
