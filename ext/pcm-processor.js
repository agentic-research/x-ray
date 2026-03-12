class PcmProcessor extends AudioWorkletProcessor {
  constructor() {
    super();
    this.buf = new Float32Array(512);
    this.pos = 0;
  }
  process(inputs) {
    const ch = inputs[0]?.[0];
    if (!ch) return true;
    for (let i = 0; i < ch.length; i++) {
      this.buf[this.pos++] = ch[i];
      if (this.pos >= 512) {
        this.port.postMessage(this.buf.slice());
        this.pos = 0;
      }
    }
    return true;
  }
}
registerProcessor('pcm-processor', PcmProcessor);
