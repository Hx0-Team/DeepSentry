// Copyright 2013 The Go Authors. All rights reserved.
// Test server derived from upstream kex.go under the preserved BSD license.
package ssh

import (
	"crypto"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"testing"
)

func TestDeepSentryLegacyGEX1024ActualExchange(t *testing.T) {
	client, server := memPipe()
	defer client.Close()
	defer server.Close()
	gex := &dhGEXSHA{hashFunc: crypto.SHA1}
	var magics handshakeMagics
	type result struct {
		value *kexResult
		err   error
	}
	done := make(chan result, 1)
	go func() {
		value, err := legacyGEX1024Server(gex, server, rand.Reader, &magics, testSigners["ecdsa"].(AlgorithmSigner), testSigners["ecdsa"].PublicKey().Type())
		done <- result{value, err}
	}()
	got, err := gex.Client(client, rand.Reader, &magics)
	if err != nil {
		t.Fatal(err)
	}
	peer := <-done
	if peer.err != nil || !reflect.DeepEqual(got, peer.value) {
		t.Fatal("1024-bit GEX transcript/key mismatch", peer.err)
	}
}
func legacyGEX1024Server(gex *dhGEXSHA, c packetConn, randSource io.Reader, magics *handshakeMagics, priv AlgorithmSigner, algo string) (result *kexResult, err error) {
	// Receive GexRequest
	packet, err := c.readPacket()
	if err != nil {
		return
	}
	var kexDHGexRequest kexDHGexRequestMsg
	if err = Unmarshal(packet, &kexDHGexRequest); err != nil {
		return
	}
	// We check that the request received is valid and that the MaxBits
	// requested are at least equal to our supported minimum. This is the same
	// check done in OpenSSH:
	// https://github.com/openssh/openssh-portable/blob/80a2f64b/kexgexs.c#L94
	//
	// Furthermore, we also check that the required MinBits are less than or
	// equal to 4096 because we can use up to Oakley Group 16.
	if kexDHGexRequest.MaxBits < kexDHGexRequest.MinBits || kexDHGexRequest.PreferredBits < kexDHGexRequest.MinBits ||
		kexDHGexRequest.MaxBits < kexDHGexRequest.PreferredBits || kexDHGexRequest.MaxBits < dhGroupExchangeMinimumBits ||
		kexDHGexRequest.MinBits > 4096 {
		return nil, fmt.Errorf("ssh: DH GEX request out of range, min: %d, max: %d, preferred: %d", kexDHGexRequest.MinBits,
			kexDHGexRequest.MaxBits, kexDHGexRequest.PreferredBits)
	}

	p := new(big.Int).Set(kexAlgoMap[InsecureKeyExchangeDH1SHA1].(*dhGroup).p)
	if err != nil {
		return nil, err
	}

	g := big.NewInt(2)
	msg := &kexDHGexGroupMsg{
		P: p,
		G: g,
	}
	if err := c.writePacket(Marshal(msg)); err != nil {
		return nil, err
	}

	// Receive GexInit
	packet, err = c.readPacket()
	if err != nil {
		return
	}
	var kexDHGexInit kexDHGexInitMsg
	if err = Unmarshal(packet, &kexDHGexInit); err != nil {
		return
	}

	pHalf := new(big.Int).Rsh(p, 1)

	y, err := rand.Int(randSource, pHalf)
	if err != nil {
		return
	}
	Y := new(big.Int).Exp(g, y, p)

	pMinusOne := new(big.Int).Sub(p, bigOne)
	if kexDHGexInit.X.Cmp(bigOne) <= 0 || kexDHGexInit.X.Cmp(pMinusOne) >= 0 {
		return nil, errors.New("ssh: DH parameter out of bounds")
	}
	kInt := new(big.Int).Exp(kexDHGexInit.X, y, p)

	hostKeyBytes := priv.PublicKey().Marshal()

	h := gex.hashFunc.New()
	magics.write(h)
	writeString(h, hostKeyBytes)
	binary.Write(h, binary.BigEndian, kexDHGexRequest.MinBits)
	binary.Write(h, binary.BigEndian, kexDHGexRequest.PreferredBits)
	binary.Write(h, binary.BigEndian, kexDHGexRequest.MaxBits)
	writeInt(h, p)
	writeInt(h, g)
	writeInt(h, kexDHGexInit.X)
	writeInt(h, Y)

	K := make([]byte, intLength(kInt))
	marshalInt(K, kInt)
	h.Write(K)

	H := h.Sum(nil)

	// H is already a hash, but the hostkey signing will apply its
	// own key-specific hash algorithm.
	sig, err := signAndMarshal(priv, randSource, H, algo)
	if err != nil {
		return nil, err
	}

	kexDHGexReply := kexDHGexReplyMsg{
		HostKey:   hostKeyBytes,
		Y:         Y,
		Signature: sig,
	}
	packet = Marshal(&kexDHGexReply)

	err = c.writePacket(packet)

	return &kexResult{
		H:         H,
		K:         K,
		HostKey:   hostKeyBytes,
		Signature: sig,
		Hash:      gex.hashFunc,
	}, err
}
