package usbgadget

import "time"

func (u *UsbGadget) resetUserInputTime() {
	u.lastUserInputLock.Lock()
	defer u.lastUserInputLock.Unlock()
	u.lastUserInput = time.Now()
}

func (u *UsbGadget) GetLastUserInputTime() time.Time {
	u.lastUserInputLock.Lock()
	defer u.lastUserInputLock.Unlock()
	return u.lastUserInput
}
