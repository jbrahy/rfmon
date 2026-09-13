"""GNU Radio 802.11 OFDM receive chain: IQ on stdin, radiotap pcap on stdout.

This script is launched and supervised by rfmon (internal/wifi.Supervisor);
it is not meant to be run by hand. It takes no command line arguments. It
reads a continuous stream of signed 8-bit interleaved I/Q samples (the
format hackrf_transfer writes) from file descriptor 0, runs the
gr-ieee802-11 short sync, long sync, FFT, equalizer and MAC decode chain,
wraps decoded MAC frames in a radiotap header via gr-foo's
wireshark_connector, and writes the resulting pcap stream to file
descriptor 1. It runs until stdin is closed by the caller.

Center frequency and sample rate match the dwell rfmon runs hackrf_transfer
at: 2437e6 Hz (WiFi channel 6), 20e6 samples/sec.
"""
from gnuradio import blocks, fft, gr
from gnuradio.fft import window
import foo
import ieee802_11

freq = 2437e6
samp_rate = 20e6
sync_length = 320
window_size = 48

tb = gr.top_block()

# Raw signed 8-bit interleaved I/Q from hackrf_transfer on stdin (fd 0).
raw = blocks.file_descriptor_source(gr.sizeof_char, 0, False)
src = blocks.interleaved_char_to_complex(False, 128.0)
tb.connect(raw, src)

mag2 = blocks.complex_to_mag_squared(1)
ma0 = blocks.moving_average_cc(window_size, 1, 4000, 1)
ma_pow = blocks.moving_average_ff(window_size + 16, 1, 4000, 1)
delay16 = blocks.delay(gr.sizeof_gr_complex, 16)
conj = blocks.conjugate_cc()
mult = blocks.multiply_vcc(1)
mag = blocks.complex_to_mag(1)
div = blocks.divide_ff(1)
sync_short = ieee802_11.sync_short(0.56, 2, False, False)
delay_sync = blocks.delay(gr.sizeof_gr_complex, sync_length)
sync_long = ieee802_11.sync_long(sync_length, False, False)
s2v = blocks.stream_to_vector(gr.sizeof_gr_complex, 64)
fftb = fft.fft_vcc(64, True, window.rectangular(64), True, 1)
eq = ieee802_11.frame_equalizer(ieee802_11.Equalizer(0), freq, samp_rate, False, False)
decode = ieee802_11.decode_mac(False, False)
ws = foo.wireshark_connector(127, False)
# Decoded frames wrapped in a radiotap header, written to stdout (fd 1).
sink = blocks.file_descriptor_sink(gr.sizeof_char, 1)

tb.connect(src, mag2, ma_pow)
tb.connect(src, (mult, 0))
tb.connect(src, delay16, conj, (mult, 1))
tb.connect(mult, ma0)
tb.connect(ma0, mag, (div, 0))
tb.connect(ma_pow, (div, 1))
tb.connect(delay16, (sync_short, 0))
tb.connect(ma0, (sync_short, 1))
tb.connect(div, (sync_short, 2))
tb.connect(sync_short, delay_sync, (sync_long, 1))
tb.connect(sync_short, (sync_long, 0))
tb.connect(sync_long, s2v, fftb, eq, decode)
tb.msg_connect((decode, "out"), (ws, "in"))
tb.connect(ws, sink)

# Runs until fd 0 (stdin) closes, then returns.
tb.run()
