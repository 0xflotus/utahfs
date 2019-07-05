package config

import (
	"fmt"
	"io/ioutil"
	"path"

	"github.com/Bren2010/utahfs"
	"github.com/Bren2010/utahfs/persistent"

	"gopkg.in/yaml.v2"
)

type Client struct {
	DataDir string `yaml:"data-dir"` // Directory where the WAL and pin file should be kept. Default: .utahfs

	StorageProvider *StorageProvider `yaml:"storage-provider"`
	MaxWALSize      int              `yaml:"max-wal-size"` // Max number of blocks to put in WAL before blocking on remote storage. Default: 64*512 blocks

	CacheSize int    `yaml:"cache-size"` // Size of in-memory LRU cache. Default: 32*1024 blocks, -1 to disable.
	Password  string `yaml:"password"`   // Password for encryption and integrity. Mandatory.

	NumPtrs  int64 `yaml:"num-ptrs"`  // Number of pointers in a file's skiplist. Default: 12
	DataSize int64 `yaml:"data-size"` // Amount of data kept in each of a file's blocks. Default: 32 KiB
}

func ClientFromFile(path string) (*Client, error) {
	raw, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	parsed := &Client{}
	if err = yaml.Unmarshal(raw, parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

func (c *Client) FS(mountPath string) (*utahfs.BlockFilesystem, error) {
	if c.DataDir == "" {
		c.DataDir = path.Join(path.Dir(mountPath), ".utahfs")
	}

	// Setup object storage.
	store, err := c.StorageProvider.Store()
	if err != nil {
		return nil, err
	}

	// Setup a local WAL.
	if c.MaxWALSize == 0 {
		c.MaxWALSize = 64 * 512
	}
	relStore, err := persistent.NewLocalWAL(store, path.Join(c.DataDir, "wal"), c.MaxWALSize)
	if err != nil {
		return nil, err
	}

	// Setup caching if desired.
	if c.CacheSize == 0 {
		c.CacheSize = 32 * 1024
	}
	if c.CacheSize != -1 {
		relStore, err = persistent.NewCache(relStore, c.CacheSize)
		if err != nil {
			return nil, err
		}
	}

	// Setup buffered block storage.
	buffered := persistent.NewBufferedStorage(relStore)
	block := persistent.NewSimpleBlock(buffered)

	// Setup encryption and integrity.
	if c.Password == "" {
		return nil, fmt.Errorf("no password given for encryption")
	}
	block, err = persistent.WithIntegrity(block, c.Password, path.Join(c.DataDir, "pin.json"))
	if err != nil {
		return nil, err
	}
	block, err = persistent.WithEncryption(block, c.Password)
	if err != nil {
		return nil, err
	}

	// Setup application storage.
	appStore := persistent.NewAppStorage(block)

	// Setup block-based filesystem.
	if c.NumPtrs == 0 {
		c.NumPtrs = 12
	}
	if c.DataSize == 0 {
		c.DataSize = 32 * 1024
	}
	bfs, err := utahfs.NewBlockFilesystem(appStore, c.NumPtrs, c.DataSize)
	if err != nil {
		return nil, err
	}

	return bfs, nil
}

type StorageProvider struct {
	B2AcctId string `yaml:"b2-acct-id"`
	B2AppKey string `yaml:"b2-app-key"`
	B2Bucket string `yaml:"b2-bucket"`
	B2Url    string `yaml:"b2-url"`

	S3AppId  string `yaml:"s3-app-id"`
	S3AppKey string `yaml:"s3-app-key"`
	S3Bucket string `yaml:"s3-bucket"`
	S3Url    string `yaml:"s3-url"`
	S3Region string `yaml:"s3-region"`

	Retry int `yaml:"retry"` // Max number of times to retry reqs that fail.
}

func (sp *StorageProvider) hasB2() bool {
	return sp.B2AcctId != "" || sp.B2AppKey != "" || sp.B2Bucket != "" || sp.B2Url != ""
}

func (sp *StorageProvider) hasS3() bool {
	return sp.S3AppId != "" || sp.S3AppKey != "" || sp.S3Bucket != "" || sp.S3Url != "" || sp.S3Region != ""
}

func (sp *StorageProvider) Store() (persistent.ObjectStorage, error) {
	if sp == nil || !sp.hasB2() && !sp.hasS3() {
		return nil, fmt.Errorf("no object storage provider defined")
	} else if sp.hasB2() && sp.hasS3() {
		return nil, fmt.Errorf("only one object storage provider may be defined")
	}

	// Connect to either B2 or S3.
	var (
		out persistent.ObjectStorage
		err error
	)
	if sp.hasB2() {
		out, err = persistent.NewB2(sp.B2AcctId, sp.B2AppKey, sp.B2Bucket, sp.B2Url)
	} else if sp.hasS3() {
		out, err = persistent.NewS3(sp.S3AppId, sp.S3AppKey, sp.S3Bucket, sp.S3Url, sp.S3Region)
	}
	if err != nil {
		return nil, err
	}

	// Configure retries if the user wants.
	if sp.Retry > 1 {
		out, err = persistent.NewRetry(out, sp.Retry)
		if err != nil {
			return nil, err
		}
	}

	return out, nil
}
