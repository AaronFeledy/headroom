#include <QtTest>

// These tests are deliberately skipped, NOT unfinished tests or TODOs.
// Do not implement button clicks, confirmation activation, transport requests,
// controller invocations, integration tests, or live probes for redemption.
// Even a single accidental request can burn a very valuable banked reset.
class ResetProhibitedTest : public QObject {
    Q_OBJECT
private slots:
    void resetButtonAndConfirmationMustNotBeTested() {
        QSKIP("DO NOT TEST the reset button or confirmation. It can burn a very valuable banked reset. This skip is intentional; do not implement or enable this test.");
    }
    void resetEndpointAndTriggeringCodeMustNotBeTested() {
        QSKIP("DO NOT TEST the reset endpoint or any code that might trigger a reset, including controller and SSH/HTTP paths. It can burn a very valuable banked reset. This skip is intentional.");
    }
    void resetLatchAndDelayedRefreshMustNotBeTestedThroughRedemption() {
        QSKIP("DO NOT TEST the reset latch or delayed refresh by clicking a reset button, confirming redemption, invoking the reset controller, or calling the endpoint, even with a mocked transport. A reset is very valuable. Review this flow statically; this skip must not be implemented or enabled.");
    }
    void resetTransportMustNeverBeRetriedOrTested() {
        QSKIP("DO NOT TEST or add retry behavior to reset submission, upload replay, SSH forwarding, or the provider POST. Do not simulate failed reset requests or consume a reset to verify that retries are disabled. This intentional skip must remain unimplemented and disabled.");
    }
    void claudeResetButtonAndConfirmationMustNotBeTested() {
        QSKIP("DO NOT TEST the Claude reset button, Use now, or confirmation. It can burn a very valuable banked reset. This skip is intentional; do not implement or enable this test.");
    }
    void claudeAutomaticResetMustNotBeTested() {
        QSKIP("DO NOT TEST Claude automatic reset arming or firing, even with fixtures or fakes. It can burn a very valuable banked reset. This skip is intentional; do not implement or enable this test.");
    }
    void claudeResetReceiptAndTransportsMustNotBeTestedThroughRedemption() {
        QSKIP("DO NOT TEST Claude reset submission, the reset endpoint, durable receipts, latch release, HTTP(S), or SSH through redemption, even with mocked transports. A reset is very valuable; do not implement or enable this test.");
    }
};

QTEST_APPLESS_MAIN(ResetProhibitedTest)
#include "test_reset_prohibited.moc"
