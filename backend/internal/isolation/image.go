package isolation

// ApprovedImageSHA256 pins the complete manifest (kernel, initramfs, QEMU,
// firmware and DLLs). Updating it requires source review and real VM tests.
const ApprovedImageSHA256 = "4215bb05ca9d8c1c86ca109caf39889f47f7d798ec8382d4916cbbc3f637ae7d"
