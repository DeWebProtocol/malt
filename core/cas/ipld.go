// Package cas provides Content Addressable Storage clients.
package cas

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"

	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// IPLDCodec identifies the IPLD codec type.
type IPLDCodec uint64

const (
	// IPLDCodecRaw is raw binary data
	IPLDCodecRaw IPLDCodec = 0x55
	// IPLDCodecDagCBOR is DAG-CBOR (CBOR with CIDs)
	IPLDCodecDagCBOR IPLDCodec = 0x71
	// IPLDCodecDagJSON is DAG-JSON (JSON with CIDs)
	IPLDCodecDagJSON IPLDCodec = 0x0201
)

// IPLDNode represents a parsed IPLD node.
type IPLDNode struct {
	// Links are the CID links found in the node
	Links []LinkInfo

	// Data is the raw data (for leaf nodes)
	Data []byte

	// Fields are named fields (for map nodes)
	Fields map[string]interface{}
}

// LinkInfo represents a link found in an IPLD node.
type LinkInfo struct {
	// Name is the field name or path segment
	Name string

	// CID is the target CID
	CID cid.Cid

	// Size is the size of the linked block (if known)
	Size uint64
}

// IPLDParser parses IPLD blocks and extracts links.
type IPLDParser struct {
	cas Reader
}

// NewIPLDParser creates a new IPLD parser.
func NewIPLDParser(cas Reader) *IPLDParser {
	return &IPLDParser{cas: cas}
}

// ParseBlock parses a block and returns the IPLD node structure.
func (p *IPLDParser) ParseBlock(c cid.Cid, data []byte) (*IPLDNode, error) {
	codec := IPLDCodec(c.Type())

	switch codec {
	case IPLDCodecRaw:
		return p.parseRaw(data)
	case IPLDCodecDagCBOR:
		return p.parseCBOR(data)
	case IPLDCodecDagJSON:
		return p.parseJSON(data)
	default:
		// Try CBOR first, then fall back to raw
		node, err := p.parseCBOR(data)
		if err != nil {
			return p.parseRaw(data)
		}
		return node, nil
	}
}

// parseRaw parses raw binary data.
func (p *IPLDParser) parseRaw(data []byte) (*IPLDNode, error) {
	return &IPLDNode{
		Data: data,
	}, nil
}

// parseCBOR parses CBOR/DAG-CBOR encoded data.
// Implements a simple CBOR parser that extracts CID links.
func (p *IPLDParser) parseCBOR(data []byte) (*IPLDNode, error) {
	node := &IPLDNode{
		Fields: make(map[string]interface{}),
	}

	// Simple CBOR parsing
	// CBOR major types:
	// 0: unsigned integer
	// 1: negative integer
	// 2: byte string
	// 3: text string
	// 4: array
	// 5: map
	// 6: tag
	// 7: simple/float

	decoder := newCBORDecoder(data)
	result, err := decoder.decode()
	if err != nil {
		return nil, fmt.Errorf("failed to decode CBOR: %w", err)
	}

	// Process result
	switch v := result.(type) {
	case map[string]interface{}:
		node.Fields = v
		p.extractLinksFromMap(v, node)
	case []interface{}:
		for i, item := range v {
			node.Fields[fmt.Sprintf("%d", i)] = item
		}
		p.extractLinksFromArray(v, node)
	case []byte:
		node.Data = v
	}

	return node, nil
}

// parseJSON parses JSON/DAG-JSON encoded data.
func (p *IPLDParser) parseJSON(data []byte) (*IPLDNode, error) {
	node := &IPLDNode{
		Fields: make(map[string]interface{}),
	}

	var parsed interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("failed to decode JSON: %w", err)
	}

	switch v := parsed.(type) {
	case map[string]interface{}:
		node.Fields = v
		p.extractLinksFromMap(v, node)
	case []interface{}:
		for i, item := range v {
			node.Fields[fmt.Sprintf("%d", i)] = item
		}
		p.extractLinksFromArray(v, node)
	}

	return node, nil
}

// extractLinksFromMap extracts CID links from a map.
func (p *IPLDParser) extractLinksFromMap(m map[string]interface{}, node *IPLDNode) {
	for k, v := range m {
		// Check for DAG-JSON/DAG-CBOR link format: {"/": "cid"}
		if subMap, ok := v.(map[string]interface{}); ok {
			if linkStr, ok := subMap["/"].(string); ok {
				if c, err := cid.Decode(linkStr); err == nil {
					node.Links = append(node.Links, LinkInfo{
						Name: k,
						CID:  c,
					})
				}
			}
		}
		// Check for nested arrays
		if arr, ok := v.([]interface{}); ok {
			p.extractLinksFromArray(arr, node)
		}
	}
}

// extractLinksFromArray extracts CID links from an array.
func (p *IPLDParser) extractLinksFromArray(arr []interface{}, node *IPLDNode) {
	for i, v := range arr {
		if subMap, ok := v.(map[string]interface{}); ok {
			if linkStr, ok := subMap["/"].(string); ok {
				if c, err := cid.Decode(linkStr); err == nil {
					node.Links = append(node.Links, LinkInfo{
						Name: fmt.Sprintf("%d", i),
						CID:  c,
					})
				}
			}
		}
	}
}

// ResolveLink resolves a link by name from an IPLD node.
func (p *IPLDParser) ResolveLink(node *IPLDNode, name string) (cid.Cid, bool) {
	for _, link := range node.Links {
		if link.Name == name {
			return link.CID, true
		}
	}
	return cid.Cid{}, false
}

// GetAllLinks returns all links from an IPLD node.
func (p *IPLDParser) GetAllLinks(node *IPLDNode) []LinkInfo {
	return node.Links
}

// FollowLink fetches a linked block and parses it.
func (p *IPLDParser) FollowLink(c cid.Cid, linkName string) (*IPLDNode, error) {
	// Get the block data
	data, err := p.cas.Get(nil, c)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch block: %w", err)
	}

	// Parse the block
	node, err := p.ParseBlock(c, data)
	if err != nil {
		return nil, fmt.Errorf("failed to parse block: %w", err)
	}

	// Find the link
	targetCID, ok := p.ResolveLink(node, linkName)
	if !ok {
		return nil, fmt.Errorf("link %s not found", linkName)
	}

	// Fetch and parse target
	targetData, err := p.cas.Get(nil, targetCID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch linked block: %w", err)
	}

	return p.ParseBlock(targetCID, targetData)
}

// Simple CBOR decoder
type cborDecoder struct {
	data []byte
	pos  int
}

func newCBORDecoder(data []byte) *cborDecoder {
	return &cborDecoder{data: data}
}

func (d *cborDecoder) decode() (interface{}, error) {
	if d.pos >= len(d.data) {
		return nil, fmt.Errorf("unexpected end of data")
	}

	b := d.data[d.pos]
	d.pos++

	majorType := b >> 5
	additionalInfo := b & 0x1f

	switch majorType {
	case 0: // unsigned integer
		return d.decodeUint(additionalInfo)
	case 1: // negative integer
		v, err := d.decodeUint(additionalInfo)
		if err != nil {
			return nil, err
		}
		return -(v.(uint64)) - 1, nil
	case 2: // byte string
		length, err := d.decodeLength(additionalInfo)
		if err != nil {
			return nil, err
		}
		if d.pos+int(length) > len(d.data) {
			return nil, fmt.Errorf("byte string length exceeds data")
		}
		result := make([]byte, length)
		copy(result, d.data[d.pos:d.pos+int(length)])
		d.pos += int(length)
		return result, nil
	case 3: // text string
		length, err := d.decodeLength(additionalInfo)
		if err != nil {
			return nil, err
		}
		if d.pos+int(length) > len(d.data) {
			return nil, fmt.Errorf("text string length exceeds data")
		}
		result := string(d.data[d.pos : d.pos+int(length)])
		d.pos += int(length)
		return result, nil
	case 4: // array
		length, err := d.decodeLength(additionalInfo)
		if err != nil {
			return nil, err
		}
		arr := make([]interface{}, length)
		for i := uint64(0); i < length; i++ {
			v, err := d.decode()
			if err != nil {
				return nil, err
			}
			arr[i] = v
		}
		return arr, nil
	case 5: // map
		length, err := d.decodeLength(additionalInfo)
		if err != nil {
			return nil, err
		}
		m := make(map[string]interface{})
		for i := uint64(0); i < length; i++ {
			k, err := d.decode()
			if err != nil {
				return nil, err
			}
			key, ok := k.(string)
			if !ok {
				return nil, fmt.Errorf("map key is not a string")
			}
			v, err := d.decode()
			if err != nil {
				return nil, err
			}
			m[key] = v
		}
		return m, nil
	case 6: // tag
		tag, err := d.decodeLength(additionalInfo)
		if err != nil {
			return nil, err
		}
		// For CID tag (42), decode the following byte string as CID
		if tag == 42 {
			v, err := d.decode()
			if err != nil {
				return nil, err
			}
			if bs, ok := v.([]byte); ok {
				// CID is encoded with a leading 0x00
				if len(bs) > 0 && bs[0] == 0 {
					c, err := cid.Cast(bs[1:])
					if err != nil {
						return nil, err
					}
					return map[string]interface{}{"/": c.String()}, nil
				}
			}
			return v, nil
		}
		// Skip tag value
		return d.decode()
	case 7: // simple/float
		return d.decodeSimple(additionalInfo)
	default:
		return nil, fmt.Errorf("unknown major type: %d", majorType)
	}
}

func (d *cborDecoder) decodeUint(additionalInfo byte) (interface{}, error) {
	switch additionalInfo {
	case 24:
		if d.pos >= len(d.data) {
			return nil, fmt.Errorf("unexpected end of data")
		}
		v := d.data[d.pos]
		d.pos++
		return uint64(v), nil
	case 25:
		if d.pos+2 > len(d.data) {
			return nil, fmt.Errorf("unexpected end of data")
		}
		v := binary.BigEndian.Uint16(d.data[d.pos:])
		d.pos += 2
		return uint64(v), nil
	case 26:
		if d.pos+4 > len(d.data) {
			return nil, fmt.Errorf("unexpected end of data")
		}
		v := binary.BigEndian.Uint32(d.data[d.pos:])
		d.pos += 4
		return uint64(v), nil
	case 27:
		if d.pos+8 > len(d.data) {
			return nil, fmt.Errorf("unexpected end of data")
		}
		v := binary.BigEndian.Uint64(d.data[d.pos:])
		d.pos += 8
		return v, nil
	default:
		if additionalInfo < 24 {
			return uint64(additionalInfo), nil
		}
		return nil, fmt.Errorf("invalid additional info for uint: %d", additionalInfo)
	}
}

func (d *cborDecoder) decodeLength(additionalInfo byte) (uint64, error) {
	v, err := d.decodeUint(additionalInfo)
	if err != nil {
		return 0, err
	}
	return v.(uint64), nil
}

func (d *cborDecoder) decodeSimple(additionalInfo byte) (interface{}, error) {
	switch additionalInfo {
	case 20:
		return false, nil
	case 21:
		return true, nil
	case 22:
		return nil, nil
	default:
		return nil, nil
	}
}

// CreateDAGJSON creates a DAG-JSON block from a map.
func CreateDAGJSON(fields map[string]interface{}) ([]byte, cid.Cid, error) {
	// Convert CIDs to link format
	for k, v := range fields {
		if c, ok := v.(cid.Cid); ok {
			fields[k] = map[string]string{"/": c.String()}
		}
	}

	data, err := json.Marshal(fields)
	if err != nil {
		return nil, cid.Cid{}, fmt.Errorf("failed to marshal: %w", err)
	}

	// Create CID
	mhash, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		return nil, cid.Cid{}, fmt.Errorf("failed to create hash: %w", err)
	}

	c := cid.NewCidV1(uint64(IPLDCodecDagJSON), mhash)
	return data, c, nil
}

// CreateRawBlock creates a raw block from data.
func CreateRawBlock(data []byte) ([]byte, cid.Cid, error) {
	mhash, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		return nil, cid.Cid{}, fmt.Errorf("failed to create hash: %w", err)
	}

	c := cid.NewCidV1(uint64(IPLDCodecRaw), mhash)
	return data, c, nil
}

// EncodeVarint encodes a varint.
func EncodeVarint(n uint64) []byte {
	var buf [binary.MaxVarintLen64]byte
	size := binary.PutUvarint(buf[:], n)
	return buf[:size]
}

// DecodeVarint decodes a varint.
func DecodeVarint(data []byte) (uint64, int, error) {
	n, size := binary.Uvarint(data)
	if size <= 0 {
		return 0, 0, bytes.ErrTooLarge
	}
	return n, size, nil
}
