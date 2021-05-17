// IRIS Endpoint-Server (EPS)
// Copyright (C) 2021-2021 The IRIS Endpoint-Server Authors (see AUTHORS.md)
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, either version 3 of the
// License, or (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package sd

import (
	"crypto/x509"
	"encoding/json"
	"fmt"
	"github.com/iris-gateway/eps"
	"github.com/iris-gateway/eps/helpers"
	"sync"
)

const (
	SignedChangeRecordEntry uint8 = 1
)

type RecordDirectorySettings struct {
	DatabaseFile      string `json:"database_file"`
	CACertificateFile string `json:"ca_certificate_file"`
}

type RecordDirectory struct {
	rootCert       *x509.Certificate
	dataStore      helpers.DataStore
	settings       *RecordDirectorySettings
	entries        map[string]*eps.DirectoryEntry
	recordsByHash  map[string]*eps.SignedChangeRecord
	recordChildren map[string][]*eps.SignedChangeRecord
	orderedRecords []*eps.SignedChangeRecord
	mutex          sync.Mutex
}

func MakeRecordDirectory(settings *RecordDirectorySettings) (*RecordDirectory, error) {

	cert, err := helpers.LoadCertificate(settings.CACertificateFile, false)

	if err != nil {
		return nil, err
	}

	f := &RecordDirectory{
		rootCert:       cert,
		orderedRecords: make([]*eps.SignedChangeRecord, 0),
		recordsByHash:  make(map[string]*eps.SignedChangeRecord),
		recordChildren: make(map[string][]*eps.SignedChangeRecord),
		settings:       settings,
		dataStore:      helpers.MakeFileDataStore(settings.DatabaseFile),
	}

	if err := f.dataStore.Init(); err != nil {
		return nil, err
	}

	_, err = f.update()

	return f, err
}

func (f *RecordDirectory) Entry(name string) (*eps.DirectoryEntry, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	if entry, ok := f.entries[name]; !ok {
		return nil, nil
	} else {
		return entry, nil
	}
}

func (f *RecordDirectory) AllEntries() ([]*eps.DirectoryEntry, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	entries := make([]*eps.DirectoryEntry, len(f.entries))
	i := 0
	for _, entry := range f.entries {
		entries[i] = entry
		i++
	}
	return entries, nil
}

func (f *RecordDirectory) Entries(*eps.DirectoryQuery) ([]*eps.DirectoryEntry, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	return nil, nil
}

// determines whether a subject can append to the service directory
func (f *RecordDirectory) canAppend(record *eps.SignedChangeRecord) (bool, error) {

	cert, err := helpers.LoadCertificateFromString(record.Signature.Certificate, true)

	if err != nil {
		return false, err
	}

	subjectInfo, err := helpers.GetSubjectInfo(cert)

	if err != nil {
		return false, err
	}

	// operators can edit their own channels and set their own preferences
	if subjectInfo.Name == record.Record.Name && (record.Record.Section == "channels" || record.Record.Section == "preferences") {
		return true, nil
	}

	for _, group := range subjectInfo.Groups {
		if group == "sd-admin" {
			// service directory admins can do everything
			return true, nil
		}
	}

	// we verify the signature and hash of the record
	if ok, err := helpers.VerifyRecord(record, f.orderedRecords, f.rootCert); err != nil {
		return false, err
	} else if !ok {
		return false, nil
	}

	// everything else is forbidden
	return false, nil
}

// Appends a new record
func (f *RecordDirectory) Append(record *eps.SignedChangeRecord) error {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	if _, err := f.update(); err != nil {
		return err
	}

	if ok, err := f.canAppend(record); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("you cannot append")
	}

	tip, err := f.tip()

	if err != nil {
		return err
	}

	if (tip != nil && record.ParentHash != tip.Hash) || (tip == nil && record.ParentHash != "") {
		return fmt.Errorf("stale append, please try again")
	}

	id, err := helpers.RandomID(16)

	if err != nil {
		return err
	}

	rawData, err := json.Marshal(record)

	if err != nil {
		return err
	}

	dataEntry := &helpers.DataEntry{
		Type: SignedChangeRecordEntry,
		ID:   id,
		Data: rawData,
	}

	if err := f.dataStore.Write(dataEntry); err != nil {
		return err
	}

	// we update the store
	if newRecords, err := f.update(); err != nil {
		return err
	} else {
		for _, newRecord := range newRecords {
			if newRecord.Hash == record.Hash {
				return nil
			}
		}
		return fmt.Errorf("new record not found")
	}
}

func (f *RecordDirectory) Update() error {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	_, err := f.update()
	return err
}

// Returns the latest record
func (f *RecordDirectory) Tip() (*eps.SignedChangeRecord, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	return f.tip()
}

func (f *RecordDirectory) tip() (*eps.SignedChangeRecord, error) {
	if len(f.orderedRecords) == 0 {
		return nil, nil
	}
	return f.orderedRecords[len(f.orderedRecords)-1], nil
}

// Returns all records since a given date
func (f *RecordDirectory) Records(since string) ([]*eps.SignedChangeRecord, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	relevantRecords := make([]*eps.SignedChangeRecord, 0)
	found := false
	if since == "" {
		found = true
	}
	for _, record := range f.orderedRecords {
		if record.Hash == since {
			found = true
		}
		if found {
			relevantRecords = append(relevantRecords, record)
		}
	}
	return relevantRecords, nil
}

// Integrates a record into the directory
func (f *RecordDirectory) integrate(record *eps.SignedChangeRecord) error {
	entry, ok := f.entries[record.Record.Name]
	if !ok {
		entry = eps.MakeDirectoryEntry()
		entry.Name = record.Record.Name
	}
	if err := helpers.IntegrateChangeRecord(record, entry); err != nil {
		return err
	}
	f.entries[record.Record.Name] = entry
	return nil
}

// picks the best record from a series of alternatives (based on chain length)
func (f *RecordDirectory) buildChains(records []*eps.SignedChangeRecord, visited map[string]bool) ([][]*eps.SignedChangeRecord, error) {

	chains := make([][]*eps.SignedChangeRecord, 0)

	for _, record := range records {
		if _, ok := visited[record.Hash]; ok {
			return nil, fmt.Errorf("circular relationship detected")
		} else {
			visited[record.Hash] = true
		}
		chain := make([]*eps.SignedChangeRecord, 1)
		chain[0] = record
		children, ok := f.recordChildren[record.Hash]
		if ok {
			childChains, err := f.buildChains(children, visited)
			if err != nil {
				return nil, err
			}
			for _, childChain := range childChains {
				fullChain := append(chain, childChain...)
				chains = append(chains, fullChain)
			}
		} else {
			chains = append(chains, chain)
		}
	}

	return chains, nil

}

func (f *RecordDirectory) update() ([]*eps.SignedChangeRecord, error) {
	if entries, err := f.dataStore.Read(); err != nil {
		return nil, err
	} else {
		changeRecords := make([]*eps.SignedChangeRecord, 0, len(entries))
		for _, entry := range entries {
			switch entry.Type {
			case SignedChangeRecordEntry:
				record := &eps.SignedChangeRecord{}
				if err := json.Unmarshal(entry.Data, &record); err != nil {
					return nil, fmt.Errorf("invalid record format!")
				}
				changeRecords = append(changeRecords, record)
			default:
				return nil, fmt.Errorf("unknown entry type found...")
			}
		}

		for _, record := range changeRecords {
			f.recordsByHash[record.Hash] = record
		}

		for _, record := range changeRecords {
			var parentHash string
			// if a parent exists we set the hash to it. Records without
			// parent (root records) will be stored under the empty hash...
			if parentRecord, ok := f.recordsByHash[record.ParentHash]; ok {
				parentHash = parentRecord.Hash
			}
			children, ok := f.recordChildren[parentHash]
			if !ok {
				children = make([]*eps.SignedChangeRecord, 0)
			}
			children = append(children, record)
			f.recordChildren[parentHash] = children
		}

		rootRecords, ok := f.recordChildren[""]

		// no records present it seems
		if !ok {
			return nil, nil
		}

		chains, err := f.buildChains(rootRecords, map[string]bool{})

		eps.Log.Infof("Found %d chains, %d root records", len(chains), len(rootRecords))

		if err != nil {
			return nil, err
		}

		verifiedChains := make([][]*eps.SignedChangeRecord, 0)
		for i, chain := range chains {
			valid := true
			for j, record := range chain {
				eps.Log.Infof("Chain %d, record %d: %s", i, j, record.Hash)
				// we verify the signature of the record
				if ok, err := helpers.VerifyRecord(record, chain[:j], f.rootCert); err != nil {
					return nil, err
				} else if !ok {
					eps.Log.Warning("signature does not match, ignoring this chain...")
					valid = false
					break
				}
			}
			if valid {
				verifiedChains = append(verifiedChains, chain)
			}
		}

		eps.Log.Infof("%d verified chains", len(verifiedChains))

		var bestChain []*eps.SignedChangeRecord
		var maxLength int
		for _, chain := range verifiedChains {
			if len(chain) > maxLength {
				bestChain = chain
				maxLength = len(chain)
			}
		}

		eps.Log.Infof("Best chain with length %d", len(bestChain))

		if bestChain == nil {
			return nil, nil
		}

		// we store the ordered sequence of records
		f.orderedRecords = bestChain

		// we regenerate the entries based on the new set of records
		f.entries = make(map[string]*eps.DirectoryEntry)
		for _, record := range bestChain {
			f.integrate(record)
		}

		return bestChain, nil
	}
}
