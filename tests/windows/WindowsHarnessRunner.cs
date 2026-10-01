internal static class WindowsHarnessRunner
{
    public static async Task RunAsync()
    {
        var tests = new ServerProcessManagerTests();
        await tests.Test_RemoteMode_when_ApiUrlConfigured();
        await tests.Test_AttachFirst_when_DefaultHealthHealthy();
        await tests.Test_AcquiresBinary_when_MissingBeforeSpawn();
        await tests.Test_StartsOwned_when_HealthBecomesReady();
        await tests.Test_CleansUpOwnedProcess_when_ReadinessFails();
        await tests.Test_ConcurrentEnsure_when_LocalStartNeeded();
        await tests.Test_RestartsOwnedProcess_when_ItExits();
        await tests.Test_DoesNotRestart_when_Attached();
        await tests.Test_Dispose_when_OwnedAndAttached();
        await tests.Test_LauncherUsesListenAddrFlag_when_BuildingStartInfo();
        await tests.Test_AssignerThrow_when_ProcessAlreadyLaunched();
        await tests.Test_DisposeRace_when_ExitEventArrives();
        await tests.Test_StaleExitEvent_when_ProcessWasRestartedManually();
        await tests.Test_AcquisitionCleansTemp_when_DownloadFails();
        await tests.Test_AcquisitionWritesVersionManifest_when_DownloadSucceeds();
        await tests.Test_HealthProbeAcceptsHealthyServer_when_VersionDiffers();
        await tests.Test_HealthProbeAcceptsDegradedServer_when_ProviderErroring();
        await tests.Test_AcquisitionCancellation_when_BeforeLaunch();
        await tests.Test_ReadinessCancellation_when_AfterLaunch();
        await tests.Test_ReadinessException_when_AfterLaunch();
        await tests.Test_AcquirerRethrowsCancellation_and_CleansTemp();
        await tests.Test_RestartMonitorFailure_when_AcquirerThrows();
        await tests.Test_RestartMonitorFailure_when_ReadinessThrows();
        await tests.Test_UpdateSwapsAppAndServer_when_BothUpdatesExist();
        await tests.Test_UpdateStopsOwnedServer_before_ServerSwap();
        await tests.Test_UpdateRollsBackBothBinaries_when_ServerSwapFails();
        await tests.Test_UpdateKeepsServer_when_OnlyTrayUpdateExists();
        await TrayApiHarnessRunner.RunAsync();
        var snapshotTests = new BrowserCookieSnapshotTests();
        await snapshotTests.Test_SnapshotCopiesWalDatabaseAndCleansArtifacts();
        await snapshotTests.Test_SnapshotCleansArtifactsWhenOpenFails();
        await snapshotTests.Test_ReaderUsesDomainBoundaryExpiryAndProfilePriority();
        await snapshotTests.Test_ReaderUsesFirefoxWalSnapshotAndDeterministicProfiles();
        await snapshotTests.Test_ReaderSkipsUnsupportedAndMalformedEncryption();
        await snapshotTests.Test_ReaderContinuesAfterMalformedProfile();
        await snapshotTests.Test_ReaderDiscoversFirefoxProfilesLazily();
        await snapshotTests.Test_HelperProtocolAcceptsOnlyKnownProviderAndBoundsOutput();
        await snapshotTests.Test_HelperJsonShapeSourceAndNulls();
        await snapshotTests.Test_InstalledBrowsersOrderAndSourcePriority();
        await snapshotTests.Test_ChromiumStatusesAndProfileAggregation();
        await snapshotTests.Test_LockedAndStatusPrecedence();
        await snapshotTests.Test_FirefoxDefaultsAbsolutePathsAndStatuses();
        await snapshotTests.Test_ReaderDecryptsNativeDpapiAndAesFixturesOnWindows();
        var iconOwnershipTests = new IconOwnershipTests();
        await iconOwnershipTests.Test_NativeIconLeaseDisposesHandleExactlyOnce();
        Console.WriteLine("ServerProcessManager tests passed");
    }
}
