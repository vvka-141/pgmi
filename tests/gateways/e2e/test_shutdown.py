"""SIGTERM drains: the gateway stops taking connections and answers the
requests already in flight before it exits."""

import threading
import time
import unittest

from harness import request, world


class ShutdownTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.world = world()

    def test_sigterm_finishes_the_request_in_flight(self):
        gateway = self.world.start_gateway()
        answers = []
        caller = threading.Thread(target=lambda: answers.append(request(gateway.base, "GET", "/api/transport/slow")))
        caller.start()
        deadline = time.monotonic() + 5
        while not self.world.backends_sleeping() and time.monotonic() < deadline:
            time.sleep(0.05)
        self.assertTrue(self.world.backends_sleeping(), "the slow request never reached the database")
        gateway.process.terminate()
        caller.join(30)
        self.assertEqual([a.status for a in answers], [200], [a.body for a in answers])
        gateway.process.wait(30)


if __name__ == "__main__":
    unittest.main()
