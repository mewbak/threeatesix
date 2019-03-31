package intel8086

import (
	"github.com/andrewjc/threeatesix/common"
	"github.com/andrewjc/threeatesix/devices/bus"
	"github.com/andrewjc/threeatesix/devices/io"
	"github.com/andrewjc/threeatesix/devices/memmap"
	"log"
)

func New80386CPU() *CpuCore {

	cpuCore := &CpuCore{}
	cpuCore.partId = common.MODULE_PRIMARY_PROCESSOR

	cpuCore.registers = &CpuRegisters{}

	// index of 8 bit registers
	cpuCore.registers.registers8Bit = []*uint8{
		&cpuCore.registers.AL,
		&cpuCore.registers.CL,
		&cpuCore.registers.DL,
		&cpuCore.registers.BL,
		&cpuCore.registers.AH,
		&cpuCore.registers.CH,
		&cpuCore.registers.DH,
		&cpuCore.registers.BH,
	}

	// index of 16 bit registers
	cpuCore.registers.registers16Bit = []*uint16{
		&cpuCore.registers.AX,
		&cpuCore.registers.CX,
		&cpuCore.registers.DX,
		&cpuCore.registers.BX,
		&cpuCore.registers.SP,
		&cpuCore.registers.BP,
		&cpuCore.registers.SI,
		&cpuCore.registers.DI,
	}

	cpuCore.registers.registersSegmentRegisters = []*uint16{
		&cpuCore.registers.ES,
		&cpuCore.registers.CS,
		&cpuCore.registers.SS,
		&cpuCore.registers.DS,
	}

	cpuCore.opCodeMap = make([]OpCodeImpl, 256)

	mapOpCodes(cpuCore)

	return cpuCore
}

type CpuCore struct {
	partId                 uint8
	bus                    *bus.Bus
	memoryAccessController *memmap.MemoryAccessController
	ioPortAccessController   *io.IOPortAccessController

	registers *CpuRegisters
	opCodeMap []OpCodeImpl
	mode      uint8
	flags CpuExecutionFlags

	busId uint32

	currentlyExecutingInstructionPointer uint32
	lastExecutedInstructionPointer       uint32

	currentByteAtCodePointer byte
}

type CpuExecutionFlags struct {
	CS_OVERRIDE        uint16
	CS_OVERRIDE_ENABLE bool

	CR0                uint32
}

func (device *CpuCore) SetDeviceBusId(id uint32) {
	device.busId = id
}

func (device *CpuCore) OnReceiveMessage(message bus.BusMessage) {
	switch {
	case message.Subject == common.MESSAGE_REQUEST_CPU_MODESWITCH:
		device.EnterMode(message.Data[0])
	}
}

func (core *CpuCore) SetCS(addr uint16) {
	core.registers.CS = addr
}

func (core *CpuCore) SetIP(addr uint16) {
	core.registers.IP = addr
}

func (core *CpuCore) GetIP() uint16 {
	return core.registers.IP
}

func (core *CpuCore) GetCS() uint16 {
	return core.registers.CS
}

func (core *CpuCore) IncrementIP() {
	core.registers.IP++
}

func (core *CpuCore) Init(bus *bus.Bus) {
	core.bus = bus

	// obtain a pointer to the memory controller on the bus
	// this is a bit of a hack but avoids a linear lookup for every
	// instruction access
	dev1 := core.bus.FindSingleDevice(common.MODULE_MEMORY_ACCESS_CONTROLLER).(*memmap.MemoryAccessController)
	core.memoryAccessController = dev1

	dev2 := core.bus.FindSingleDevice(common.MODULE_IO_PORT_ACCESS_CONTROLLER).(*io.IOPortAccessController)
	core.ioPortAccessController = dev2


	core.EnterMode(common.REAL_MODE)

	core.Reset()
}

func (core *CpuCore) Reset() {
	core.registers.CS = 0xF000
	core.registers.IP = 0xFFF0
	core.bus.SendMessage(bus.BusMessage{common.MESSAGE_GLOBAL_LOCK_BIOS_MEM_REGION, []byte{}})
}

func (core *CpuCore) EnterMode(mode uint8) {
	core.mode = mode

	core.bus.SendMessage(bus.BusMessage{common.MESSAGE_GLOBAL_CPU_MODESWITCH, []byte{mode}})

	processorString := core.FriendlyPartName()
	modeString := ""
	if core.mode == common.REAL_MODE {
		modeString = "REAL MODE"
	} else if core.mode == common.PROTECTED_MODE {
		modeString = "PROTECTED MODE"
	}
	log.Printf("%s entered %s\r\n", processorString, modeString)
}

// Gets the current code segment + IP addr in memory
func (core *CpuCore) GetCurrentCodePointer() uint32 {
	return uint32(core.registers.CS<<4 + core.registers.IP)
}

// Returns the address in memory of the instruction currently executing.
// This is different from GetCurrentCodePointer in that the currently executing
// instruction can update the CS and IP registers.
func (core *CpuCore) GetCurrentlyExecutingInstructionPointer() uint32 {
	return core.currentlyExecutingInstructionPointer
}

func (core *CpuCore) Step() {
	core.currentlyExecutingInstructionPointer = core.GetCurrentCodePointer()
	if core.currentlyExecutingInstructionPointer == core.lastExecutedInstructionPointer {
		log.Fatalf("CPU appears to be in a loop! Did you forget to increment the IP register?")
	}

	status := core.routeInstruction()

	if status != 0 {
		panic(0)
	}

	core.lastExecutedInstructionPointer = core.currentlyExecutingInstructionPointer

}

func (core *CpuCore) FriendlyPartName() string {
	if core.partId == common.MODULE_PRIMARY_PROCESSOR {
		return "PRIMARY PROCESSOR"
	}

	if core.partId == common.MODULE_MATH_CO_PROCESSOR {
		return "MATH CO PROCESSOR"
	}

	return "Unknown"
}

func (core *CpuCore) readImm8() uint8 {
	retVal := core.memoryAccessController.ReadAddr8(uint32(core.GetCurrentCodePointer()))
	core.registers.IP += 1
	return retVal
}

func (core *CpuCore) readImm16() uint16 {
	retVal := core.memoryAccessController.ReadAddr16(uint32(core.GetCurrentCodePointer()))
	core.registers.IP += 2
	return retVal
}

func (core *CpuCore) BuildAddress(segment uint16, address uint16) uint32 {
	return uint32(segment<<4 + address)
}

func (core *CpuCore) readRm8(modrm *ModRm) (*uint8, string) {
	if modrm.mod == 3 {
		dest := core.registers.registers8Bit[modrm.rm]
		destName := core.registers.index8ToString(modrm.rm)
		return dest, destName

	} else {
		addressMode := modrm.getAddressMode16(core)
		destValue := core.memoryAccessController.ReadAddr8(uint32(addressMode))
		destName := "rm/8"
		return &destValue, destName
	}
}

func (core *CpuCore) readRm16(modrm *ModRm) (*uint16, string) {
	if modrm.mod == 3 {
		dest := core.registers.registers16Bit[modrm.rm]
		destName := core.registers.index16ToString(modrm.rm)
		return dest, destName

	} else {
		addressMode := modrm.getAddressMode16(core)
		destValue := core.memoryAccessController.ReadAddr16(uint32(addressMode))
		destName := "rm/16"
		return &destValue, destName
	}
}

func (core *CpuCore) readR8(modrm *ModRm) (*uint8, string) {
	dest := core.registers.registers8Bit[modrm.reg]
	dstName := core.registers.index8ToString(modrm.reg)
	return dest, dstName
}

func (core *CpuCore) readR16(modrm *ModRm) (*uint16, string) {
	dest := core.registers.registers16Bit[modrm.reg]
	dstName := core.registers.index16ToString(modrm.reg)
	return dest, dstName

}

func (core *CpuCore) writeRm8(modrm *ModRm, value *uint8) {
	if modrm.mod == 3 {
		core.registers.registers8Bit[modrm.rm] = value
	} else {
		addressMode := modrm.getAddressMode16(core)
		core.memoryAccessController.WriteAddr8(uint32(addressMode), *value)
	}
}

func (core *CpuCore) writeRm16(modrm *ModRm, value *uint16) {
	if modrm.mod == 3 {
		core.registers.registers16Bit[modrm.rm] = value
	} else {
		addressMode := modrm.getAddressMode16(core)
		core.memoryAccessController.WriteAddr16(uint32(addressMode), *value)
	}
}

func (core *CpuCore) writeR8(modrm *ModRm, value *uint8) {
	core.registers.registers8Bit[modrm.reg] = value
}

func (core *CpuCore) writeR16(modrm *ModRm, value *uint16) {
	core.registers.registers16Bit[modrm.reg] = value
}

func (core *CpuCore) SetFlag(mask uint16, status bool) {
	core.registers.SetFlag(mask, status)
}

func (core *CpuCore) GetFlag(mask uint16) bool {
	return core.registers.GetFlag(mask)
}

func (core *CpuCore) GetFlagInt(mask uint16) uint16 {
	return core.registers.GetFlagInt(mask)
}

func (core *CpuCore) GetRegisters() *CpuRegisters {
	return core.registers
}
