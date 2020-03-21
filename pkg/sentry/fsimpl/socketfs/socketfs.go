// Copyright 2020 The gVisor Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package socketfs provides a filesystem implementation for anonymous sockets.
package socketfs

import (
	"fmt"

	"gvisor.dev/gvisor/pkg/abi/linux"
	"gvisor.dev/gvisor/pkg/context"
	"gvisor.dev/gvisor/pkg/sentry/fsimpl/kernfs"
	"gvisor.dev/gvisor/pkg/sentry/kernel/auth"
	"gvisor.dev/gvisor/pkg/sentry/vfs"
	"gvisor.dev/gvisor/pkg/syserror"
)

// filesystem implements vfs.FilesystemImpl.
type filesystem struct {
	kernfs.Filesystem
}

// SetupMount returns a new disconnected mount for sockets in vfsObj.
func SetupMount(vfsObj *vfs.VirtualFilesystem) error {
	fs := &filesystem{}
	fs.Init(vfsObj)
	vfsfs := fs.VFSFilesystem()
	// NewDisconnectedMount will take an additional reference on vfsfs.
	defer vfsfs.DecRef()
	sm, err := vfsObj.NewDisconnectedMount(vfsfs, nil, &vfs.MountOptions{})
	if err != nil {
		return fmt.Errorf("failed to set up VirtualFilesystem.SocketMount: %v", err)
	}

	vfsObj.SocketMount = sm
	return nil
}

// NewDentry creates and returns a new Dentry in the global socket mount.
func NewDentry(ctx context.Context, vfsObj *vfs.VirtualFilesystem, mode linux.FileMode) *vfs.Dentry {
	mnt := vfsObj.SocketMount
	fs, ok := mnt.Filesystem().Impl().(*kernfs.Filesystem)
	if !ok {
		panic("vfsObj.SockMount is not backed by socketfs")
	}

	creds := auth.CredentialsFromContext(ctx)
	ino := fs.NextIno()
	filemode := linux.FileMode(linux.S_IFSOCK | (mode & linux.PermissionsMask))
	i := &inode{}
	i.Init(creds, ino, filemode)

	d := &kernfs.Dentry{}
	d.Init(i)
	return d.VFSDentry()
}

// inode implements kernfs.Inode.
type inode struct {
	kernfs.InodeNotDirectory
	kernfs.InodeNotSymlink
	kernfs.InodeAttrs
	kernfs.InodeNoopRefCount
}

// Open implements kernfs.Inode.Open.
func (i *inode) Open(rp *vfs.ResolvingPath, vfsd *vfs.Dentry, opts vfs.OpenOptions) (*vfs.FileDescription, error) {
	return nil, syserror.ENXIO
}
