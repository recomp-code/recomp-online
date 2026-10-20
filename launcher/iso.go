package main

// Minimal ISO 9660 reader: enough to find and copy a few files by name from the player's disc image.

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strings"
)

const isoSector = 2048

type isoEntry struct {
	Name string
	LBA  int64
	Size int64
	Dir  bool
}

type isoImage struct {
	f        *os.File
	rootLBA  int64
	rootSize int64
}

func openISO(path string) (*isoImage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	pvd := make([]byte, isoSector)
	if _, err := f.ReadAt(pvd, 16*isoSector); err != nil {
		f.Close()
		return nil, errors.New("no es una imagen de disco válida")
	}
	if pvd[0] != 1 || string(pvd[1:6]) != "CD001" {
		f.Close()
		return nil, errors.New("no es una imagen ISO 9660")
	}
	return &isoImage{f: f, rootLBA: int64(binary.LittleEndian.Uint32(pvd[156+2:])), rootSize: int64(binary.LittleEndian.Uint32(pvd[156+10:]))}, nil
}

func (im *isoImage) Close() error { return im.f.Close() }

func (im *isoImage) list(lba, size int64) ([]isoEntry, error) {
	if size <= 0 || size > 16<<20 {
		return nil, errors.New("directorio inválido en la ISO")
	}
	d := make([]byte, size)
	if _, err := im.f.ReadAt(d, lba*isoSector); err != nil && err != io.EOF {
		return nil, err
	}
	var out []isoEntry
	for p := 0; p < len(d); {
		n := int(d[p])
		if n == 0 {
			p = (p/isoSector + 1) * isoSector
			continue
		}
		if p+33 > len(d) || p+n > len(d) {
			break
		}
		nameLen := int(d[p+32])
		if p+33+nameLen > len(d) {
			break
		}
		raw := d[p+33 : p+33+nameLen]
		if !(nameLen == 1 && (raw[0] == 0 || raw[0] == 1)) {
			name := string(raw)
			if i := strings.IndexByte(name, ';'); i >= 0 {
				name = name[:i]
			}
			out = append(out, isoEntry{
				Name: strings.TrimSuffix(name, "."),
				LBA:  int64(binary.LittleEndian.Uint32(d[p+2:])),
				Size: int64(binary.LittleEndian.Uint32(d[p+10:])),
				Dir:  d[p+25]&2 != 0,
			})
		}
		p += n
	}
	return out, nil
}

func (im *isoImage) root() ([]isoEntry, error) { return im.list(im.rootLBA, im.rootSize) }

func findEntry(dir []isoEntry, name string) *isoEntry {
	for i := range dir {
		if strings.EqualFold(dir[i].Name, name) {
			return &dir[i]
		}
	}
	return nil
}

func (im *isoImage) read(e *isoEntry) ([]byte, error) {
	if e.Size > 64<<20 {
		return nil, errors.New("archivo demasiado grande")
	}
	b := make([]byte, e.Size)
	_, err := im.f.ReadAt(b, e.LBA*isoSector)
	if err == io.EOF {
		err = nil
	}
	return b, err
}
