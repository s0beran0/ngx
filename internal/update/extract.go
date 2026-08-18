package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"path"
	"strings"
)

// binaryLimit caps what we accept to extract from the archive into memory.
// A small compressed file can declare a huge member; with no ceiling,
// extraction becomes unbounded memory consumption.
const binaryLimit = 256 << 20 // 256 MiB

// ExtractBinary takes the executable out of the archive published in the
// release. goreleaser packages tar.gz on Unix and zip on Windows.
//
// Extraction happens ONLY AFTER Verify: decompressing unauthenticated data is
// handing the attacker a parser to attack before the signature has even been
// checked.
func ExtractBinary(fileName string, data []byte, goos string) ([]byte, error) {
	target := "ngx"
	if goos == "windows" {
		target = "ngx.exe"
	}

	switch {
	case strings.HasSuffix(fileName, ".tar.gz"), strings.HasSuffix(fileName, ".tgz"):
		return fromTarGz(fileName, data, target)
	case strings.HasSuffix(fileName, ".zip"):
		return fromZip(fileName, data, target)
	default:
		return nil, newError(CodeInvalidArtifact,
			"the artifact %s is not in a format the update knows how to open (.tar.gz "+
				"or .zip); download the new version manually", fileName)
	}
}

func fromTarGz(fileName string, data []byte, target string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, wrapError(err, CodeInvalidArtifact,
			"the artifact %s cannot be decompressed", fileName)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, wrapError(err, CodeInvalidArtifact,
				"the artifact %s is truncated or corrupted", fileName)
		}
		if h.Typeflag != tar.TypeReg || path.Base(h.Name) != target {
			continue
		}
		bin, err := io.ReadAll(io.LimitReader(tr, binaryLimit+1))
		if err != nil {
			return nil, wrapError(err, CodeInvalidArtifact,
				"could not read %s from inside %s", target, fileName)
		}
		if int64(len(bin)) > binaryLimit {
			return nil, newError(CodeInvalidArtifact,
				"the binary inside %s went past the accepted size limit", fileName)
		}
		return bin, nil
	}
	return nil, newError(CodeInvalidArtifact,
		"the artifact %s does not contain the executable %s", fileName, target)
}

func fromZip(fileName string, data []byte, target string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, wrapError(err, CodeInvalidArtifact,
			"the artifact %s cannot be opened as a zip", fileName)
	}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || path.Base(f.Name) != target {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, wrapError(err, CodeInvalidArtifact,
				"could not open %s inside %s", target, fileName)
		}
		bin, err := io.ReadAll(io.LimitReader(rc, binaryLimit+1))
		rc.Close()
		if err != nil {
			return nil, wrapError(err, CodeInvalidArtifact,
				"could not read %s from inside %s", target, fileName)
		}
		if int64(len(bin)) > binaryLimit {
			return nil, newError(CodeInvalidArtifact,
				"the binary inside %s went past the accepted size limit", fileName)
		}
		return bin, nil
	}
	return nil, newError(CodeInvalidArtifact,
		"the artifact %s does not contain the executable %s", fileName, target)
}
