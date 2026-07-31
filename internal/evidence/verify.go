package evidence

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Status is the overall verdict of verifying an evidence log.
type Status int

// Verification verdicts (evidence-schema.md §9). Damage means unparseable
// crash artifacts were found; signed content was still fully verified.
const (
	StatusValid Status = iota
	StatusValidWithDamage
	StatusInvalid
)

// String returns the verdict name as printed by `probavi evidence verify`.
func (s Status) String() string {
	switch s {
	case StatusValid:
		return "VALID"
	case StatusValidWithDamage:
		return "VALID_WITH_DAMAGE"
	case StatusInvalid:
		return "INVALID"
	default:
		return fmt.Sprintf("Status(%d)", int(s))
	}
}

// Result reports the outcome of verifying an evidence log.
type Result struct {
	Status       Status
	Records      int   // valid records verified
	DamagedLines []int // 1-based line numbers of unparseable fragments
	FailedLine   int   // 1-based line of the first invalid record, 0 if none
	Reason       string
}

// Verify checks an evidence log against evidence-schema.md §9 using the
// given keyring. The returned error reports I/O problems only; integrity
// verdicts are in the Result.
func Verify(r io.Reader, keyring Keyring) (*Result, error) {
	if keyring == nil {
		keyring = Keyring{}
	}
	w, err := walk(r, keyring)
	if err != nil {
		return nil, err
	}
	res := &Result{
		Records:      w.records,
		DamagedLines: w.damaged,
		FailedLine:   w.failedLine,
		Reason:       w.reason,
	}
	switch {
	case w.failedLine != 0:
		res.Status = StatusInvalid
	case len(w.damaged) > 0:
		res.Status = StatusValidWithDamage
	default:
		res.Status = StatusValid
	}
	return res, nil
}

// walkState carries chain verification state across lines. With a nil
// keyring, signature checks are skipped (writer-side chain scan).
type walkState struct {
	keyring    Keyring
	nextSeq    int64
	prevHash   string
	records    int
	damaged    []int
	failedLine int
	reason     string
}

// walk runs the §9 algorithm over every line of the log. It stops at the
// first invalid record; damage (unparseable lines) is collected and
// skipped without advancing the chain.
func walk(r io.Reader, keyring Keyring) (*walkState, error) {
	w := &walkState{keyring: keyring, nextSeq: 1, prevHash: GenesisPrevHash}
	br := bufio.NewReaderSize(r, 64*1024)
	for lineNo := 1; ; lineNo++ {
		line, err := br.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			if len(line) > 0 {
				w.damaged = append(w.damaged, lineNo) // torn tail, no newline
			}
			return w, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read evidence log: %w", err)
		}
		if !w.step(bytes.TrimSuffix(line, []byte("\n")), lineNo) {
			return w, nil
		}
	}
}

// step processes one complete line; it reports false when walking must stop
// because the chain is invalid.
func (w *walkState) step(line []byte, lineNo int) bool {
	rec, verdict := w.parseCanonical(line, lineNo)
	if rec == nil {
		return verdict
	}
	if rec.Seq != w.nextSeq {
		return w.invalid(lineNo, fmt.Sprintf("seq %d, want %d", rec.Seq, w.nextSeq))
	}
	if rec.PrevHash != w.prevHash {
		return w.invalid(lineNo, fmt.Sprintf("prev_hash mismatch, want %s", w.prevHash))
	}
	if w.keyring != nil {
		if err := w.verifySignature(rec); err != nil {
			return w.invalid(lineNo, err.Error())
		}
	}
	w.prevHash = lineHash(line)
	w.nextSeq++
	w.records++
	return true
}

// parseCanonical classifies a line: damage (nil record, keep walking),
// invalid (nil record, stop), or a parsed record in canonical form.
func (w *walkState) parseCanonical(line []byte, lineNo int) (*Record, bool) {
	if len(line) > MaxRecordBytes {
		return nil, w.invalid(lineNo, ErrRecordTooLarge.Error())
	}
	v, err := decodeStrict(line)
	if err != nil {
		w.damaged = append(w.damaged, lineNo)
		return nil, true
	}
	canonical, err := Canonicalize(v)
	if err != nil {
		return nil, w.invalid(lineNo, err.Error())
	}
	if !bytes.Equal(canonical, line) {
		return nil, w.invalid(lineNo, ErrNotCanonical.Error())
	}
	var rec Record
	if err := json.Unmarshal(line, &rec); err != nil {
		return nil, w.invalid(lineNo, fmt.Sprintf("decode record: %v", err))
	}
	if rec.Schema != SchemaID {
		return nil, w.invalid(lineNo, fmt.Sprintf("unsupported schema %q", rec.Schema))
	}
	return &rec, true
}

func (w *walkState) verifySignature(rec *Record) error {
	if rec.Sig == nil {
		return errors.New("record has no sig")
	}
	if rec.Sig.Alg != "ed25519" {
		return fmt.Errorf("unsupported sig.alg %q", rec.Sig.Alg)
	}
	pub, ok := w.keyring[rec.Sig.KeyID]
	if !ok {
		return fmt.Errorf("%w %q", ErrUnknownKey, rec.Sig.KeyID)
	}
	sig, err := base64.StdEncoding.DecodeString(rec.Sig.SigB64)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("malformed sig.sig_b64")
	}
	unsigned := *rec
	unsigned.Sig = nil
	message, err := CanonicalizeRecord(&unsigned)
	if err != nil {
		return fmt.Errorf("rebuild signed bytes: %w", err)
	}
	if !ed25519.Verify(pub, message, sig) {
		return errors.New("signature verification failed")
	}
	return nil
}

func (w *walkState) invalid(lineNo int, reason string) bool {
	w.failedLine = lineNo
	w.reason = reason
	return false
}
