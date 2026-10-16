package ldap

import (
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"

	ber "github.com/go-asn1-ber/asn1-ber"
	"github.com/go-ldap/ldap/v3"
)

// fakeDir is a small LDAP server in memory: simple bind, subtree and base
// searches with and/or/not/equality/presence filters, the paged results
// control, Active Directory's ranged attribute retrieval, and modify.
type fakeDir struct {
	mu       sync.Mutex
	entries  []*fakeEntry
	bindDN   string
	password string
	// maxRange, when set, is how many values of a multi-valued attribute
	// one read returns, like Active Directory's MaxValRange.
	maxRange int
	modifies []string
	pages    int
	url      string
}

type fakeEntry struct {
	dn    string
	attrs []*ldap.EntryAttribute
}

func (e *fakeEntry) get(name string) *ldap.EntryAttribute {
	for _, a := range e.attrs {
		if strings.EqualFold(a.Name, name) {
			return a
		}
	}
	return nil
}

func (e *fakeEntry) set(name string, vals ...string) {
	if a := e.get(name); a != nil {
		a.Values = vals
		a.ByteValues = bytesOf(vals)
		return
	}
	e.attrs = append(e.attrs, &ldap.EntryAttribute{Name: name, Values: vals, ByteValues: bytesOf(vals)})
}

func (e *fakeEntry) unset(name string) {
	for i, a := range e.attrs {
		if strings.EqualFold(a.Name, name) {
			e.attrs = append(e.attrs[:i], e.attrs[i+1:]...)
			return
		}
	}
}

func bytesOf(vals []string) [][]byte {
	out := make([][]byte, len(vals))
	for i, v := range vals {
		out[i] = []byte(v)
	}
	return out
}

func (d *fakeDir) add(dn string, attrs map[string][]string) *fakeEntry {
	e := &fakeEntry{dn: dn}
	for k, v := range attrs {
		e.set(k, v...)
	}
	d.entries = append(d.entries, e)
	return e
}

func (d *fakeDir) entry(dn string) *fakeEntry {
	for _, e := range d.entries {
		if strings.EqualFold(e.dn, dn) {
			return e
		}
	}
	return nil
}

func (d *fakeDir) start(t *testing.T) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	d.url = "ldap://" + l.Addr().String()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go d.serve(c)
		}
	}()
}

func (d *fakeDir) serve(c net.Conn) {
	defer c.Close()
	for {
		p, err := ber.ReadPacket(c)
		if err != nil || len(p.Children) < 2 {
			return
		}
		id := p.Children[0].Value.(int64)
		op := p.Children[1]
		var controls []ldap.Control
		if len(p.Children) > 2 {
			for _, cp := range p.Children[2].Children {
				if ctl, err := ldap.DecodeControl(cp); err == nil {
					controls = append(controls, ctl)
				}
			}
		}
		var out []*ber.Packet
		d.mu.Lock()
		switch op.Tag {
		case ldap.ApplicationBindRequest:
			code := uint16(ldap.LDAPResultInvalidCredentials)
			if op.Children[1].Value.(string) == d.bindDN && op.Children[2].Data.String() == d.password {
				code = ldap.LDAPResultSuccess
			}
			out = append(out, message(id, result(ldap.ApplicationBindResponse, code, "")))
		case ldap.ApplicationUnbindRequest:
			d.mu.Unlock()
			return
		case ldap.ApplicationSearchRequest:
			out = d.search(id, op, controls)
		case ldap.ApplicationModifyRequest:
			out = append(out, message(id, result(ldap.ApplicationModifyResponse, d.modify(op), "")))
		case ldap.ApplicationExtendedRequest:
			out = append(out, message(id, result(ldap.ApplicationExtendedResponse, d.extended(op), "")))
		default:
			out = append(out, message(id, result(ldap.ApplicationExtendedResponse, ldap.LDAPResultProtocolError, "unsupported")))
		}
		d.mu.Unlock()
		for _, m := range out {
			if _, err := c.Write(m.Bytes()); err != nil {
				return
			}
		}
	}
}

func message(id int64, op *ber.Packet, controls ...ldap.Control) *ber.Packet {
	m := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSequence, nil, "LDAP Response")
	m.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagInteger, id, "MessageID"))
	m.AppendChild(op)
	if len(controls) > 0 {
		cs := ber.Encode(ber.ClassContext, ber.TypeConstructed, 0, nil, "Controls")
		for _, c := range controls {
			cs.AppendChild(c.Encode())
		}
		m.AppendChild(cs)
	}
	return m
}

func result(tag ber.Tag, code uint16, msg string) *ber.Packet {
	r := ber.Encode(ber.ClassApplication, ber.TypeConstructed, tag, nil, "Result")
	r.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagEnumerated, int64(code), "resultCode"))
	r.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, "", "matchedDN"))
	r.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, msg, "diagnosticMessage"))
	return r
}

func (d *fakeDir) search(id int64, op *ber.Packet, controls []ldap.Control) []*ber.Packet {
	base := op.Children[0].Value.(string)
	scope := op.Children[1].Value.(int64)
	sizeLimit := int(op.Children[3].Value.(int64))
	filter := op.Children[6]
	var attrs []string
	for _, a := range op.Children[7].Children {
		attrs = append(attrs, a.Value.(string))
	}
	var matches []*fakeEntry
	for _, e := range d.entries {
		inScope := strings.EqualFold(e.dn, base)
		if scope == ldap.ScopeWholeSubtree {
			inScope = inScope || strings.HasSuffix(strings.ToLower(e.dn), ","+strings.ToLower(base))
		}
		if inScope && eval(filter, e) {
			matches = append(matches, e)
		}
	}
	code := uint16(ldap.LDAPResultSuccess)
	if sizeLimit > 0 && len(matches) > sizeLimit {
		matches, code = matches[:sizeLimit], ldap.LDAPResultSizeLimitExceeded
	}
	var done []ldap.Control
	if c, ok := ldap.FindControl(controls, ldap.ControlTypePaging).(*ldap.ControlPaging); ok && c.PagingSize > 0 {
		d.pages++
		start, _ := strconv.Atoi(string(c.Cookie))
		end := min(start+int(c.PagingSize), len(matches))
		next := ldap.NewControlPaging(0)
		if end < len(matches) {
			next.SetCookie([]byte(strconv.Itoa(end)))
		}
		matches, done = matches[start:end], []ldap.Control{next}
	}
	var out []*ber.Packet
	for _, e := range matches {
		r := ber.Encode(ber.ClassApplication, ber.TypeConstructed, ldap.ApplicationSearchResultEntry, nil, "Entry")
		r.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, e.dn, "DN"))
		list := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSequence, nil, "Attributes")
		for _, want := range attrs {
			name, opt, _ := strings.Cut(want, ";")
			a := e.get(name)
			if a == nil {
				continue
			}
			vals, label := a.ByteValues, a.Name
			lo := 0
			if r, ok := strings.CutPrefix(opt, "range="); ok {
				lo, _ = strconv.Atoi(strings.TrimSuffix(r, "-*"))
				opt = "ranged"
			}
			if d.maxRange > 0 && (opt == "ranged" || len(vals) > d.maxRange) {
				hi := min(lo+d.maxRange, len(vals))
				end := strconv.Itoa(hi - 1)
				if hi == len(vals) {
					end = "*"
				}
				vals, label = vals[lo:hi], a.Name+";range="+strconv.Itoa(lo)+"-"+end
			}
			pa := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSequence, nil, "Attribute")
			pa.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, label, "Type"))
			set := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSet, nil, "Values")
			for _, v := range vals {
				set.AppendChild(ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, string(v), "Value"))
			}
			pa.AppendChild(set)
			list.AppendChild(pa)
		}
		r.AppendChild(list)
		out = append(out, message(id, r))
	}
	return append(out, message(id, result(ldap.ApplicationSearchResultDone, code, ""), done...))
}

func eval(f *ber.Packet, e *fakeEntry) bool {
	switch f.Tag {
	case ldap.FilterAnd:
		for _, c := range f.Children {
			if !eval(c, e) {
				return false
			}
		}
		return true
	case ldap.FilterOr:
		for _, c := range f.Children {
			if eval(c, e) {
				return true
			}
		}
		return false
	case ldap.FilterNot:
		return !eval(f.Children[0], e)
	case ldap.FilterEqualityMatch:
		name, want := f.Children[0].Value.(string), f.Children[1].Value.(string)
		a := e.get(name)
		if a == nil {
			return false
		}
		for _, v := range a.ByteValues {
			if string(v) == want || (!strings.EqualFold(name, "objectGUID") && same(name, string(v), want)) {
				return true
			}
		}
		return false
	case ldap.FilterPresent:
		name := f.Data.String()
		return strings.EqualFold(name, "objectClass") || e.get(name) != nil
	}
	return false
}

func (d *fakeDir) modify(op *ber.Packet) uint16 {
	dn := op.Children[0].Value.(string)
	e := d.entry(dn)
	if e == nil {
		return ldap.LDAPResultNoSuchObject
	}
	for _, ch := range op.Children[1].Children {
		kind := ch.Children[0].Value.(int64)
		name := ch.Children[1].Children[0].Value.(string)
		var vals []string
		for _, v := range ch.Children[1].Children[1].Children {
			vals = append(vals, v.Value.(string))
		}
		var have []string
		if a := e.get(name); a != nil {
			have = a.Values
		}
		has := func(v string) int {
			for i, h := range have {
				if same(name, h, v) {
					return i
				}
			}
			return -1
		}
		switch kind {
		case ldap.AddAttribute:
			for _, v := range vals {
				if has(v) >= 0 {
					return ldap.LDAPResultAttributeOrValueExists
				}
				have = append(have, v)
			}
			e.set(name, have...)
		case ldap.DeleteAttribute:
			if have == nil {
				return ldap.LDAPResultNoSuchAttribute
			}
			if len(vals) == 0 {
				e.unset(name)
				break
			}
			for _, v := range vals {
				i := has(v)
				if i < 0 {
					return ldap.LDAPResultNoSuchAttribute
				}
				have = append(have[:i], have[i+1:]...)
			}
			e.set(name, have...)
		case ldap.ReplaceAttribute:
			e.set(name, vals...)
		}
		d.modifies = append(d.modifies, strings.Join([]string{dn, name, strconv.FormatInt(kind, 10), strings.Join(vals, "|")}, " "))
	}
	return ldap.LDAPResultSuccess
}

// extended runs the Password Modify extended operation (RFC 3062), storing
// the new password as userPassword in the clear: enough to check it arrived.
func (d *fakeDir) extended(op *ber.Packet) uint16 {
	if len(op.Children) < 2 || op.Children[0].Data.String() != "1.3.6.1.4.1.4203.1.11.1" {
		return ldap.LDAPResultProtocolError
	}
	val, err := ber.DecodePacketErr(op.Children[1].Data.Bytes())
	if err != nil {
		return ldap.LDAPResultProtocolError
	}
	var dn, password string
	for _, ch := range val.Children {
		switch ch.Tag {
		case 0:
			dn = ch.Data.String()
		case 2:
			password = ch.Data.String()
		}
	}
	e := d.entry(dn)
	if e == nil {
		return ldap.LDAPResultNoSuchObject
	}
	e.set("userPassword", password)
	d.modifies = append(d.modifies, dn+" userPassword passwd-modify")
	return ldap.LDAPResultSuccess
}

// same compares values the way a directory does: case-insensitively, and DN
// valued attributes as DNs.
func same(attr, a, b string) bool {
	if strings.EqualFold(attr, "member") {
		return normDN(a) == normDN(b)
	}
	return strings.EqualFold(a, b)
}
