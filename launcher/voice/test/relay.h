// The relay for the tests: the server's voice protocol (server/voice.go), with loss and jitter on demand.

typedef struct {
	int64_t due;
	int len;
	struct sockaddr_in to;
	uint8_t b[1600];
} delayed;

static struct {
	int sock, port;
	volatile int running;
	struct sockaddr_in addr[RV_SLOTS + 1];
	int has[RV_SLOTS + 1];
	volatile int loss_pct, jit_min_ms, jit_max_ms;
	volatile int from[RV_SLOTS + 1]; // voice datagrams received per slot
	delayed q[8192];
	int qn;
	pthread_t th;
} R;

static const char *tok(int slot) {
	static char t[RV_SLOTS + 1][33];
	snprintf(t[slot], 33, "%032d", slot);
	return t[slot];
}

static void *relay(void *arg) {
	(void)arg;
	uint8_t b[1600];
	while (R.running) {
		struct sockaddr_in from;
		socklen_t fl = sizeof from;
		int n = (int)recvfrom(R.sock, b, sizeof b, 0, (struct sockaddr *)&from, &fl);
		int64_t now = rv_now_us();
		if (n == 36 && memcmp(b, "RLV1", 4) == 0) {
			for (int s = 1; s <= RV_SLOTS; s++)
				if (memcmp(b + 4, tok(s), 32) == 0) {
					R.addr[s] = from;
					R.has[s] = 1;
					uint8_t ack[5] = {'R', 'L', 'V', 'A', (uint8_t)('0' + s)};
					sendto(R.sock, ack, 5, 0, (struct sockaddr *)&from, fl);
				}
		} else if (n >= 2) {
			int s = 0;
			for (int i = 1; i <= RV_SLOTS; i++)
				if (R.has[i] && R.addr[i].sin_port == from.sin_port)
					s = i;
			if (!s)
				continue;
			if (b[0] == 'p')
				sendto(R.sock, b, (size_t)n, 0, (struct sockaddr *)&from, fl);
			else if (b[0] == 'v') {
				R.from[s]++;
				b[1] = (uint8_t)('0' + s);
				for (int d = 1; d <= RV_SLOTS; d++) {
					if (d == s || !R.has[d] || rand() % 100 < R.loss_pct || R.qn >= 8192)
						continue;
					delayed *q = &R.q[R.qn++];
					int j = R.jit_min_ms + (R.jit_max_ms > R.jit_min_ms ? rand() % (R.jit_max_ms - R.jit_min_ms + 1) : 0);
					q->due = now + j * 1000;
					q->len = n;
					q->to = R.addr[d];
					memcpy(q->b, b, (size_t)n);
				}
			}
		}
		for (int i = 0; i < R.qn;) { // out in due order, not in arrival order: jitter reorders, as the internet does
			if (R.q[i].due <= now) {
				sendto(R.sock, R.q[i].b, (size_t)R.q[i].len, 0, (struct sockaddr *)&R.q[i].to, sizeof R.q[i].to);
				R.q[i] = R.q[--R.qn];
			} else
				i++;
		}
	}
	return 0;
}

static void relay_start(void) {
	R.sock = socket(AF_INET, SOCK_DGRAM, 0);
	struct sockaddr_in a = {0};
	a.sin_family = AF_INET;
	a.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
	bind(R.sock, (struct sockaddr *)&a, sizeof a);
	socklen_t l = sizeof a;
	getsockname(R.sock, (struct sockaddr *)&a, &l);
	R.port = ntohs(a.sin_port);
	struct timeval tv = {0, 1000};
	setsockopt(R.sock, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof tv);
	R.running = 1;
	pthread_create(&R.th, NULL, relay, NULL);
}

