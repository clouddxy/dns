package dns

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/rsa"
	"encoding/hex"
	"encoding/base64"
	"hash"
	"time"
	"io"
	"big"
	"sort"
	"strings"
	"os"
)

// DNSSEC encryption algorithm codes.
const (
	// DNSSEC algorithms
	AlgRSAMD5    = 1
	AlgDH        = 2
	AlgDSA       = 3
	AlgECC       = 4
	AlgRSASHA1   = 5
	AlgRSASHA256 = 8
	AlgRSASHA512 = 10
	AlgECCGOST   = 12
)

// DNSSEC hashing codes.
const (
	HashSHA1 = iota
	HashSHA256
	HashGOST94
)

// The RRSIG needs to be converted to wireformat with some of
// the rdata (the signature) missing. Use this struct to easy
// the conversion (and re-use the pack/unpack functions.
type rrsigWireFmt struct {
	TypeCovered uint16
	Algorithm   uint8
	Labels      uint8
	OrigTtl     uint32
	Expiration  uint32
	Inception   uint32
	KeyTag      uint16
	SignerName  string "domain-name"
	/* No Signature */
}

// Used for converting DNSKEY's rdata to wirefmt.
type dnskeyWireFmt struct {
	Flags     uint16
	Protocol  uint8
	Algorithm uint8
	PubKey    string "base64"
}

// Calculate the keytag of the DNSKEY.
func (k *RR_DNSKEY) KeyTag() uint16 {
	var keytag int
	switch k.Algorithm {
	case AlgRSAMD5:
		println("Keytag RSAMD5. Todo")
		keytag = 0
	default:
                keywire := new(dnskeyWireFmt)
                keywire.Flags = k.Flags
                keywire.Protocol = k.Protocol
                keywire.Algorithm = k.Algorithm
                keywire.PubKey = k.PubKey
                wire := make([]byte, 2048) // TODO(mg) lenght!
                n, ok := packStruct(keywire, wire, 0)
		if !ok {
			return 0
		}
                wire = wire[:n]
		for i, v := range wire {
			if i&1 != 0 {
				keytag += int(v) // must be larger than uint32
			} else {
				keytag += int(v) << 8
			}
		}
		keytag += (keytag >> 16) & 0xFFFF
		keytag &= 0xFFFF
	}
	return uint16(keytag)
}

// Convert an DNSKEY record to a DS record.
func (k *RR_DNSKEY) ToDS(h int) *RR_DS {
	ds := new(RR_DS)
	ds.Hdr.Name = k.Hdr.Name
	ds.Hdr.Class = k.Hdr.Class
	ds.Hdr.Ttl = k.Hdr.Ttl
	ds.Algorithm = k.Algorithm
	ds.DigestType = uint8(h)
	ds.KeyTag = k.KeyTag()

        keywire := new(dnskeyWireFmt)
        keywire.Flags = k.Flags
        keywire.Protocol = k.Protocol
        keywire.Algorithm = k.Algorithm
        keywire.PubKey = k.PubKey
        wire := make([]byte, 2048) // TODO(mg) lenght!
        n, ok := packStruct(keywire, wire, 0)
        if !ok {
                return nil
        }
        wire = wire[:n]

        owner := make([]byte, 255)
        off, ok1 := packDomainName(k.Hdr.Name, owner, 0)
	if !ok1 {
		return nil
	}
        owner = owner[:off]
	/* 
	 * from RFC4034
	 * digest = digest_algorithm( DNSKEY owner name | DNSKEY RDATA);
	 * "|" denotes concatenation
	 * DNSKEY RDATA = Flags | Protocol | Algorithm | Public Key.
	 */
	// digest buffer
	digest := append(owner, wire...) // another copy TODO(mg)

	switch h {
	case HashSHA1:
		s := sha1.New()
		io.WriteString(s, string(digest))
		ds.Digest = hex.EncodeToString(s.Sum())
	case HashSHA256:
		s := sha256.New()
		io.WriteString(s, string(digest))
		ds.Digest = hex.EncodeToString(s.Sum())
	case HashGOST94:

	default:
		// wrong hash value
		return nil
	}
	return ds
}

// Sign rrset with k and return the signature RR. There
// is no check if rrset is a proper (RFC 2181) RRSet
func (k *RR_DNSKEY) Sign(rrset RRset, expiration, inception uint32) *RR_RRSIG {
	sig := new(RR_RRSIG)
	sig.Hdr.Name = rrset[0].Header().Name
	sig.Hdr.Class = rrset[0].Header().Class
	sig.Hdr.Rrtype = TypeRRSIG
	sig.Hdr.Ttl = rrset[0].Header().Ttl // re-use TTL of RRset
	sig.Inception = inception
	sig.Expiration = expiration
	sig.KeyTag = k.KeyTag()
	sig.SignerName = k.Hdr.Name
	sig.Labels = uint8(labelCount(rrset[0].Header().Name))
	sig.TypeCovered = rrset[0].Header().Rrtype

	sigwire := new(rrsigWireFmt)
	sigwire.TypeCovered = sig.TypeCovered
	sigwire.Algorithm = sig.Algorithm
	sigwire.Labels = sig.Labels
	sigwire.OrigTtl = sig.OrigTtl
	sigwire.Expiration = sig.Expiration
	sigwire.Inception = sig.Inception
	sigwire.KeyTag = sig.KeyTag
	sigwire.SignerName = sig.SignerName

	return nil
}

// Validate an rrset with the signature and key. This is the
// cryptographic test, the validity period most be check separately.
func (s *RR_RRSIG) Verify(k *RR_DNSKEY, rrset RRset) bool {
	// Frist the easy checks
	if s.KeyTag != k.KeyTag() {
		println(s.KeyTag)
		println(k.KeyTag())
		return false
	}
	if s.Hdr.Class != k.Hdr.Class {
		println("Class")
		return false
	}
	if s.Algorithm != k.Algorithm {
		println("Class")
		return false
	}
	if s.SignerName != k.Hdr.Name {
		println(s.SignerName)
		println(k.Hdr.Name)
		return false
	}
	for _, r := range rrset {
		if r.Header().Class != s.Hdr.Class {
			return false
		}
		if r.Header().Rrtype != s.TypeCovered {
			return false
		}
		// Number of labels. TODO(mg) add helper functions
	}
	sort.Sort(rrset)

	// RFC 4035 5.3.2.  Reconstructing the Signed Data
	// Copy the sig, except the rrsig data
	sigwire := new(rrsigWireFmt)
	sigwire.TypeCovered = s.TypeCovered
	sigwire.Algorithm = s.Algorithm
	sigwire.Labels = s.Labels
	sigwire.OrigTtl = s.OrigTtl
	sigwire.Expiration = s.Expiration
	sigwire.Inception = s.Inception
	sigwire.KeyTag = s.KeyTag
	sigwire.SignerName = s.SignerName
	// Create the desired binary blob
	signeddata := make([]byte, 4096)
	n, ok := packStruct(sigwire, signeddata, 0)
	if !ok {
		return false
	}
	signeddata = signeddata[:n]

	for _, r := range rrset {
		h := r.Header()
		// RFC 4034: 6.2.  Canonical RR Form. (2) - domain name to lowercase
		name := h.Name
		h.Name = strings.ToLower(h.Name)
		// 6.2.  Canonical RR Form. (3) - domain rdata to lowercaser
		switch h.Rrtype {
		case TypeNS, TypeCNAME, TypeSOA, TypeMB, TypeMG, TypeMR, TypePTR:
		case TypeHINFO, TypeMINFO, TypeMX /* TypeRP, TypeAFSDB, TypeRT */ :
		case TypeSIG /* TypePX, TypeNXT /* TypeNAPTR, TypeKX */ :
		case TypeSRV, /* TypeDNAME, TypeA6 */ TypeRRSIG, TypeNSEC:
			// lower case the domain rdata //

		}
		// 6.2. Canonical RR Form. (4) - wildcards, don't understand
		// 6.2. Canonical RR Form. (5) - origTTL

		ttl := h.Ttl
		h.Ttl = s.OrigTtl
                wire := make([]byte, 4096)
                off, ok1 := packRR(r, wire, 0)
                if !ok1 {
                        println("Failure to pack")
                        return false
                }
                wire = wire[:off]
		h.Ttl = ttl // restore the order in the universe
		h.Name = name
		if !ok1 {
			println("Failure to pack")
			return false
		}
		signeddata = append(signeddata, wire...)
	}

	sigbuf := s.sigBuf() // Get the binary signature data

	var err os.Error
	switch s.Algorithm {
	case AlgRSASHA1, AlgRSASHA256, AlgRSASHA512, AlgRSAMD5:
		pubkey := k.pubKeyRSA() // Get the key
		// Setup the hash as defined for this alg.
		var h hash.Hash
		var ch rsa.PKCS1v15Hash
		switch s.Algorithm {
		case AlgRSAMD5:
			h = md5.New()
			ch = rsa.HashMD5
		case AlgRSASHA1:
			h = sha1.New()
			ch = rsa.HashSHA1
		case AlgRSASHA256:
			h = sha256.New()
			ch = rsa.HashSHA256
		case AlgRSASHA512:
			h = sha512.New()
			ch = rsa.HashSHA512
		}
		io.WriteString(h, string(signeddata))
		sighash := h.Sum()
		err = rsa.VerifyPKCS1v15(pubkey, ch, sighash, sigbuf)
	case AlgDH:
	case AlgDSA:
	case AlgECC:
	case AlgECCGOST:
	}

	return err == nil
}

// Using RFC1982 calculate if a signature period is valid
func (s *RR_RRSIG) PeriodOK() bool {
	utc := time.UTC().Seconds()
	modi := (int64(s.Inception) - utc) / Year68
	mode := (int64(s.Expiration) - utc) / Year68
	ti := int64(s.Inception) + (modi * Year68)
	te := int64(s.Expiration) + (mode * Year68)
	return ti <= utc && utc <= te
}

// Return the signatures base64 encodedig sigdata as a byte slice.
func (s *RR_RRSIG) sigBuf() []byte {
	sigbuf := make([]byte, 1024) // TODO(mg) length!
	sigbuflen := base64.StdEncoding.DecodedLen(len(s.Signature))
	sigbuflen, _ = base64.StdEncoding.Decode(sigbuf[0:sigbuflen], []byte(s.Signature))
	sigbuf = sigbuf[:sigbuflen]
	return sigbuf
}

// Extract the RSA public key from the Key record
func (k *RR_DNSKEY) pubKeyRSA() *rsa.PublicKey {
	// Buffer holding the key data
	keybuf := make([]byte, 1024)
	keybuflen := base64.StdEncoding.DecodedLen(len(k.PubKey))
	keybuflen, _ = base64.StdEncoding.Decode(keybuf[0:keybuflen], []byte(k.PubKey))
	keybuf = keybuf[:keybuflen]

	// RFC 2537/3110, section 2. RSA Public KEY Resource Records
	// Length is in the 0th byte, unless its zero, then it
	// it in bytes 1 and 2 and its a 16 bit number
	explen := uint16(keybuf[0])
	keyoff := 1
	if explen == 0 {
		explen = uint16(keybuf[1])<<8 | uint16(keybuf[2])
		keyoff = 3
	}
	pubkey := new(rsa.PublicKey)
	pubkey.N = big.NewInt(0)
	shift := (explen - 1) * 8
	for i := int(explen - 1); i >= 0; i-- {
		pubkey.E += int(keybuf[keyoff+i]) << shift
		shift -= 8
	}
	pubkey.N.SetBytes(keybuf[keyoff+int(explen):])
	return pubkey
}

// Map for algorithm names.
var alg_str = map[uint8]string{
	AlgRSAMD5:    "RSAMD5",
	AlgDH:        "DH",
	AlgDSA:       "DSA",
	AlgRSASHA1:   "RSASHA1",
	AlgRSASHA256: "RSASHA256",
	AlgRSASHA512: "RSASHA512",
	AlgECCGOST:   "ECC-GOST",
}
