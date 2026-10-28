package storage

// RecreationWriter binds explicit new bytes to the same namespace whose old
// deletion receipt will be checked. It grants no deletion authority to callers.
type RecreationWriter interface {
	BindRecreationWrite() (BlobStore, string, func() error, error)
}

func (s LocalBlobs) BindRecreationWrite() (BlobStore, string, func() error, error) {
	bound, err := NewLocalRetentionStore(s.Root)
	if err != nil {
		return nil, "", nil, err
	}
	namespace := bound.RetentionNamespace()
	if namespace == "" {
		bound.Close()
		return nil, "", nil, ErrGCUnavailable
	}
	return bound, namespace, bound.Close, nil
}

func (s *LocalRetentionStore) BindRecreationWrite() (BlobStore, string, func() error, error) {
	namespace := s.RetentionNamespace()
	if namespace == "" {
		return nil, "", nil, ErrGCUnavailable
	}
	return s, namespace, nil, nil
}

func (s *S3Blobs) BindRecreationWrite() (BlobStore, string, func() error, error) {
	if s == nil || !gcHash.MatchString(s.namespace) {
		return nil, "", nil, ErrGCUnavailable
	}
	return s, s.namespace, nil, nil
}

func (s *S3RetentionStore) BindRecreationWrite() (BlobStore, string, func() error, error) {
	if s == nil || !gcHash.MatchString(s.namespace) {
		return nil, "", nil, ErrGCUnavailable
	}
	return s, s.namespace, nil, nil
}
