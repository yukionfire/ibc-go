package keeper_test

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/tendermint/tendermint/crypto/secp256k1"

	"github.com/cosmos/ibc-go/v3/modules/apps/29-fee/types"
	transfertypes "github.com/cosmos/ibc-go/v3/modules/apps/transfer/types"
	channeltypes "github.com/cosmos/ibc-go/v3/modules/core/04-channel/types"
)

func (suite *KeeperTestSuite) TestEscrowPacketFee() {
	var (
		err        error
		refundAcc  sdk.AccAddress
		ackFee     sdk.Coins
		receiveFee sdk.Coins
		timeoutFee sdk.Coins
		packetID   channeltypes.PacketId
	)

	testCases := []struct {
		name     string
		malleate func()
		expPass  bool
	}{
		{
			"success", func() {}, true,
		},
		{
			"success with existing packet fee", func() {
				fee := types.Fee{
					RecvFee:    receiveFee,
					AckFee:     ackFee,
					TimeoutFee: timeoutFee,
				}

				packetFee := types.NewPacketFee(fee, refundAcc.String(), []string{})
				feesInEscrow := types.NewPacketFees([]types.PacketFee{packetFee})

				suite.chainA.GetSimApp().IBCFeeKeeper.SetFeesInEscrow(suite.chainA.GetContext(), packetID, feesInEscrow)
			}, true,
		},
		{
			"fee not enabled on this channel", func() {
				packetID.ChannelId = "disabled_channel"
			}, false,
		},
		{
			"refundAcc does not exist", func() {
				// this acc does not exist on chainA
				refundAcc = suite.chainB.SenderAccount.GetAddress()
			}, false,
		},
		{
			"ackFee balance not found", func() {
				ackFee = invalidCoins
			}, false,
		},
		{
			"receive balance not found", func() {
				receiveFee = invalidCoins
			}, false,
		},
		{
			"timeout balance not found", func() {
				timeoutFee = invalidCoins
			}, false,
		},
	}

	for _, tc := range testCases {
		tc := tc

		suite.Run(tc.name, func() {
			suite.SetupTest()                   // reset
			suite.coordinator.Setup(suite.path) // setup channel

			// setup
			refundAcc = suite.chainA.SenderAccount.GetAddress()
			receiveFee = defaultReceiveFee
			ackFee = defaultAckFee
			timeoutFee = defaultTimeoutFee
			packetID = channeltypes.NewPacketId(suite.path.EndpointA.ChannelConfig.PortID, suite.path.EndpointA.ChannelID, uint64(1))

			tc.malleate()
			fee := types.Fee{
				RecvFee:    receiveFee,
				AckFee:     ackFee,
				TimeoutFee: timeoutFee,
			}
			packetFee := types.NewPacketFee(fee, refundAcc.String(), []string{})

			// refundAcc balance before escrow
			originalBal := suite.chainA.GetSimApp().BankKeeper.GetBalance(suite.chainA.GetContext(), refundAcc, sdk.DefaultBondDenom)

			// escrow the packet fee
			err = suite.chainA.GetSimApp().IBCFeeKeeper.EscrowPacketFee(suite.chainA.GetContext(), packetID, packetFee)

			if tc.expPass {
				feesInEscrow, found := suite.chainA.GetSimApp().IBCFeeKeeper.GetFeesInEscrow(suite.chainA.GetContext(), packetID)
				suite.Require().True(found)
				// check if the escrowed fee is set in state
				suite.Require().True(feesInEscrow.PacketFees[0].Fee.AckFee.IsEqual(fee.AckFee))
				suite.Require().True(feesInEscrow.PacketFees[0].Fee.RecvFee.IsEqual(fee.RecvFee))
				suite.Require().True(feesInEscrow.PacketFees[0].Fee.TimeoutFee.IsEqual(fee.TimeoutFee))
				// check if the fee is escrowed correctly
				hasBalance := suite.chainA.GetSimApp().BankKeeper.HasBalance(suite.chainA.GetContext(), suite.chainA.GetSimApp().IBCFeeKeeper.GetFeeModuleAddress(), sdk.Coin{Denom: sdk.DefaultBondDenom, Amount: sdk.NewInt(600)})
				suite.Require().True(hasBalance)
				expectedBal := originalBal.Amount.Sub(sdk.NewInt(600))
				// check if the refund acc has sent the fee
				hasBalance = suite.chainA.GetSimApp().BankKeeper.HasBalance(suite.chainA.GetContext(), refundAcc, sdk.Coin{Denom: sdk.DefaultBondDenom, Amount: expectedBal})
				suite.Require().True(hasBalance)
				suite.Require().NoError(err)
			} else {
				suite.Require().Error(err)
			}
		})
	}
}

func (suite *KeeperTestSuite) TestDistributeFee() {
	var (
		forwardRelayer    string
		forwardRelayerBal sdk.Coin
		reverseRelayer    sdk.AccAddress
		reverseRelayerBal sdk.Coin
		refundAcc         sdk.AccAddress
		refundAccBal      sdk.Coin
		packetFee         types.PacketFee
	)

	testCases := []struct {
		name      string
		malleate  func()
		expResult func()
	}{
		{
			"success",
			func() {},
			func() {
				// check if the reverse relayer is paid
				expectedReverseAccBal := reverseRelayerBal.Add(defaultAckFee[0]).Add(defaultAckFee[0])
				balance := suite.chainA.GetSimApp().BankKeeper.GetBalance(suite.chainA.GetContext(), reverseRelayer, sdk.DefaultBondDenom)
				suite.Require().Equal(expectedReverseAccBal, balance)

				// check if the forward relayer is paid
				forward, err := sdk.AccAddressFromBech32(forwardRelayer)
				suite.Require().NoError(err)

				expectedForwardAccBal := forwardRelayerBal.Add(defaultReceiveFee[0]).Add(defaultReceiveFee[0])
				balance = suite.chainA.GetSimApp().BankKeeper.GetBalance(suite.chainA.GetContext(), forward, sdk.DefaultBondDenom)
				suite.Require().Equal(expectedForwardAccBal, balance)

				// check if the refund acc has been refunded the timeoutFee
				expectedRefundAccBal := refundAccBal.Add(defaultTimeoutFee[0].Add(defaultTimeoutFee[0]))
				balance = suite.chainA.GetSimApp().BankKeeper.GetBalance(suite.chainA.GetContext(), refundAcc, sdk.DefaultBondDenom)
				suite.Require().Equal(expectedRefundAccBal, balance)

				// check the module acc wallet is now empty
				balance = suite.chainA.GetSimApp().BankKeeper.GetBalance(suite.chainA.GetContext(), suite.chainA.GetSimApp().IBCFeeKeeper.GetFeeModuleAddress(), sdk.DefaultBondDenom)
				suite.Require().Equal(sdk.NewCoin(sdk.DefaultBondDenom, sdk.NewInt(0)), balance)
			},
		},
		{
			"invalid forward address",
			func() {
				forwardRelayer = "invalid address"
			},
			func() {
				// check if the refund acc has been refunded the timeoutFee & recvFee
				expectedRefundAccBal := refundAccBal.Add(defaultTimeoutFee[0]).Add(defaultReceiveFee[0]).Add(defaultTimeoutFee[0]).Add(defaultReceiveFee[0])
				balance := suite.chainA.GetSimApp().BankKeeper.GetBalance(suite.chainA.GetContext(), refundAcc, sdk.DefaultBondDenom)
				suite.Require().Equal(expectedRefundAccBal, balance)
			},
		},
		{
			"invalid forward address: blocked address",
			func() {
				forwardRelayer = suite.chainA.GetSimApp().AccountKeeper.GetModuleAccount(suite.chainA.GetContext(), transfertypes.ModuleName).GetAddress().String()
			},
			func() {
				// check if the refund acc has been refunded the timeoutFee & recvFee
				expectedRefundAccBal := refundAccBal.Add(defaultTimeoutFee[0]).Add(defaultReceiveFee[0]).Add(defaultTimeoutFee[0]).Add(defaultReceiveFee[0])
				balance := suite.chainA.GetSimApp().BankKeeper.GetBalance(suite.chainA.GetContext(), refundAcc, sdk.DefaultBondDenom)
				suite.Require().Equal(expectedRefundAccBal, balance)
			},
		},
		{
			"invalid receiver address: ack fee returned to sender",
			func() {
				reverseRelayer = suite.chainA.GetSimApp().AccountKeeper.GetModuleAccount(suite.chainA.GetContext(), transfertypes.ModuleName).GetAddress()
			},
			func() {
				// check if the refund acc has been refunded the timeoutFee & ackFee
				expectedRefundAccBal := refundAccBal.Add(defaultTimeoutFee[0]).Add(defaultAckFee[0]).Add(defaultTimeoutFee[0]).Add(defaultAckFee[0])
				balance := suite.chainA.GetSimApp().BankKeeper.GetBalance(suite.chainA.GetContext(), refundAcc, sdk.DefaultBondDenom)
				suite.Require().Equal(expectedRefundAccBal, balance)
			},
		},
		{
			"invalid refund address: no-op, timeout fee remains in escrow",
			func() {
				packetFee.RefundAddress = suite.chainA.GetSimApp().AccountKeeper.GetModuleAccount(suite.chainA.GetContext(), transfertypes.ModuleName).GetAddress().String()
			},
			func() {
				// check if the module acc contains the timeoutFee
				expectedModuleAccBal := sdk.NewCoin(sdk.DefaultBondDenom, defaultTimeoutFee.Add(defaultTimeoutFee...).AmountOf(sdk.DefaultBondDenom))
				balance := suite.chainA.GetSimApp().BankKeeper.GetBalance(suite.chainA.GetContext(), suite.chainA.GetSimApp().IBCFeeKeeper.GetFeeModuleAddress(), sdk.DefaultBondDenom)
				suite.Require().Equal(expectedModuleAccBal, balance)
			},
		},
	}

	for _, tc := range testCases {
		tc := tc

		suite.Run(tc.name, func() {
			suite.SetupTest()                   // reset
			suite.coordinator.Setup(suite.path) // setup channel

			// setup accounts
			forwardRelayer = sdk.AccAddress(secp256k1.GenPrivKey().PubKey().Address()).String()
			reverseRelayer = sdk.AccAddress(secp256k1.GenPrivKey().PubKey().Address())
			refundAcc = suite.chainA.SenderAccount.GetAddress()

			packetID := channeltypes.NewPacketId(suite.path.EndpointA.ChannelConfig.PortID, suite.path.EndpointA.ChannelID, 1)
			fee := types.NewFee(defaultReceiveFee, defaultAckFee, defaultTimeoutFee)

			// escrow the packet fee & store the fee in state
			packetFee = types.NewPacketFee(fee, refundAcc.String(), []string{})
			err := suite.chainA.GetSimApp().IBCFeeKeeper.EscrowPacketFee(suite.chainA.GetContext(), packetID, packetFee)
			suite.Require().NoError(err)

			// escrow a second packet fee to test with multiple fees distributed
			err = suite.chainA.GetSimApp().IBCFeeKeeper.EscrowPacketFee(suite.chainA.GetContext(), packetID, packetFee)
			suite.Require().NoError(err)

			tc.malleate()

			// fetch the account balances before fee distribution (forward, reverse, refund)
			forwardAccAddress, _ := sdk.AccAddressFromBech32(forwardRelayer)
			forwardRelayerBal = suite.chainA.GetSimApp().BankKeeper.GetBalance(suite.chainA.GetContext(), forwardAccAddress, sdk.DefaultBondDenom)
			reverseRelayerBal = suite.chainA.GetSimApp().BankKeeper.GetBalance(suite.chainA.GetContext(), reverseRelayer, sdk.DefaultBondDenom)
			refundAccBal = suite.chainA.GetSimApp().BankKeeper.GetBalance(suite.chainA.GetContext(), refundAcc, sdk.DefaultBondDenom)

			suite.chainA.GetSimApp().IBCFeeKeeper.DistributePacketFees(suite.chainA.GetContext(), forwardRelayer, reverseRelayer, []types.PacketFee{packetFee, packetFee})

			tc.expResult()
		})
	}
}

func (suite *KeeperTestSuite) TestDistributeTimeoutFee() {
	suite.coordinator.Setup(suite.path) // setup channel

	// setup
	refundAcc := suite.chainA.SenderAccount.GetAddress()
	timeoutRelayer := sdk.AccAddress(secp256k1.GenPrivKey().PubKey().Address())

	packetID := channeltypes.NewPacketId(
		suite.path.EndpointA.ChannelConfig.PortID,
		suite.path.EndpointA.ChannelID,
		1,
	)

	fee := types.Fee{
		RecvFee:    defaultReceiveFee,
		AckFee:     defaultAckFee,
		TimeoutFee: defaultTimeoutFee,
	}

	// escrow the packet fee & store the fee in state
	packetFee := types.NewPacketFee(fee, refundAcc.String(), []string{})

	err := suite.chainA.GetSimApp().IBCFeeKeeper.EscrowPacketFee(suite.chainA.GetContext(), packetID, packetFee)
	suite.Require().NoError(err)
	// escrow a second packet fee to test with multiple fees distributed
	err = suite.chainA.GetSimApp().IBCFeeKeeper.EscrowPacketFee(suite.chainA.GetContext(), packetID, packetFee)
	suite.Require().NoError(err)

	// refundAcc balance after escrow
	refundAccBal := suite.chainA.GetSimApp().BankKeeper.GetBalance(suite.chainA.GetContext(), refundAcc, sdk.DefaultBondDenom)

	suite.chainA.GetSimApp().IBCFeeKeeper.DistributePacketFeesOnTimeout(suite.chainA.GetContext(), timeoutRelayer, []types.PacketFee{packetFee, packetFee})

	// check if the timeoutRelayer has been paid
	hasBalance := suite.chainA.GetSimApp().BankKeeper.HasBalance(suite.chainA.GetContext(), timeoutRelayer, fee.TimeoutFee[0])
	suite.Require().True(hasBalance)

	// check if the refund acc has been refunded the recv & ack fees
	expectedRefundAccBal := refundAccBal.Add(fee.AckFee[0]).Add(fee.AckFee[0])
	expectedRefundAccBal = refundAccBal.Add(fee.RecvFee[0]).Add(fee.RecvFee[0])
	hasBalance = suite.chainA.GetSimApp().BankKeeper.HasBalance(suite.chainA.GetContext(), refundAcc, expectedRefundAccBal)
	suite.Require().True(hasBalance)

	// check the module acc wallet is now empty
	hasBalance = suite.chainA.GetSimApp().BankKeeper.HasBalance(suite.chainA.GetContext(), suite.chainA.GetSimApp().IBCFeeKeeper.GetFeeModuleAddress(), sdk.Coin{Denom: sdk.DefaultBondDenom, Amount: sdk.NewInt(0)})
	suite.Require().True(hasBalance)
}

func (suite *KeeperTestSuite) TestRefundFeesOnChannelClosure() {
	var (
		expIdentifiedPacketFees []types.IdentifiedPacketFees
		expEscrowBal            sdk.Coins
		expRefundBal            sdk.Coins
		refundAcc               sdk.AccAddress
		fee                     types.Fee
		locked                  bool
	)

	testCases := []struct {
		name     string
		malleate func()
		expPass  bool
	}{
		{
			"success", func() {
				for i := 1; i < 6; i++ {
					// store the fee in state & update escrow account balance
					packetID := channeltypes.NewPacketId(suite.path.EndpointA.ChannelConfig.PortID, suite.path.EndpointA.ChannelID, uint64(i))
					packetFees := types.NewPacketFees([]types.PacketFee{types.NewPacketFee(fee, refundAcc.String(), nil)})
					identifiedPacketFees := types.NewIdentifiedPacketFees(packetID, packetFees.PacketFees)

					suite.chainA.GetSimApp().IBCFeeKeeper.SetFeesInEscrow(suite.chainA.GetContext(), packetID, packetFees)

					suite.chainA.GetSimApp().BankKeeper.SendCoinsFromAccountToModule(suite.chainA.GetContext(), refundAcc, types.ModuleName, fee.Total())

					expIdentifiedPacketFees = append(expIdentifiedPacketFees, identifiedPacketFees)
				}
			}, true,
		},
		{
			"success with undistributed packet fees on a different channel", func() {
				for i := 1; i < 6; i++ {
					// store the fee in state & update escrow account balance
					packetID := channeltypes.NewPacketId(suite.path.EndpointA.ChannelConfig.PortID, suite.path.EndpointA.ChannelID, uint64(i))
					packetFees := types.NewPacketFees([]types.PacketFee{types.NewPacketFee(fee, refundAcc.String(), nil)})
					identifiedPacketFees := types.NewIdentifiedPacketFees(packetID, packetFees.PacketFees)

					suite.chainA.GetSimApp().IBCFeeKeeper.SetFeesInEscrow(suite.chainA.GetContext(), packetID, packetFees)

					suite.chainA.GetSimApp().BankKeeper.SendCoinsFromAccountToModule(suite.chainA.GetContext(), refundAcc, types.ModuleName, fee.Total())

					expIdentifiedPacketFees = append(expIdentifiedPacketFees, identifiedPacketFees)
				}

				// set packet fee for a different channel
				packetID := channeltypes.NewPacketId(suite.path.EndpointA.ChannelConfig.PortID, "channel-1", uint64(1))
				packetFees := types.NewPacketFees([]types.PacketFee{types.NewPacketFee(fee, refundAcc.String(), nil)})
				suite.chainA.GetSimApp().IBCFeeKeeper.SetFeeEnabled(suite.chainA.GetContext(), suite.path.EndpointA.ChannelConfig.PortID, "channel-1")

				suite.chainA.GetSimApp().IBCFeeKeeper.SetFeesInEscrow(suite.chainA.GetContext(), packetID, packetFees)
				suite.chainA.GetSimApp().BankKeeper.SendCoinsFromAccountToModule(suite.chainA.GetContext(), refundAcc, types.ModuleName, fee.Total())

				expEscrowBal = fee.Total()
				expRefundBal = expRefundBal.Sub(fee.Total())
			}, true,
		},
		{
			"escrow account empty, module should become locked", func() {
				locked = true

				// store the fee in state without updating escrow account balance
				packetID := channeltypes.NewPacketId(suite.path.EndpointA.ChannelConfig.PortID, suite.path.EndpointA.ChannelID, uint64(1))
				packetFees := types.NewPacketFees([]types.PacketFee{types.NewPacketFee(fee, refundAcc.String(), nil)})
				identifiedPacketFees := types.NewIdentifiedPacketFees(packetID, packetFees.PacketFees)

				suite.chainA.GetSimApp().IBCFeeKeeper.SetFeesInEscrow(suite.chainA.GetContext(), packetID, packetFees)

				expIdentifiedPacketFees = []types.IdentifiedPacketFees{identifiedPacketFees}
			},
			true,
		},
		{
			"escrow account goes negative on second packet, module should become locked", func() {
				locked = true

				// store 2 fees in state
				packetID1 := channeltypes.NewPacketId(suite.path.EndpointA.ChannelConfig.PortID, suite.path.EndpointA.ChannelID, uint64(1))
				packetID2 := channeltypes.NewPacketId(suite.path.EndpointA.ChannelConfig.PortID, suite.path.EndpointA.ChannelID, uint64(2))
				packetFees := types.NewPacketFees([]types.PacketFee{types.NewPacketFee(fee, refundAcc.String(), nil)})
				identifiedPacketFee1 := types.NewIdentifiedPacketFees(packetID1, packetFees.PacketFees)
				identifiedPacketFee2 := types.NewIdentifiedPacketFees(packetID2, packetFees.PacketFees)

				suite.chainA.GetSimApp().IBCFeeKeeper.SetFeesInEscrow(suite.chainA.GetContext(), packetID1, packetFees)
				suite.chainA.GetSimApp().IBCFeeKeeper.SetFeesInEscrow(suite.chainA.GetContext(), packetID2, packetFees)

				// update escrow account balance for 1 fee
				suite.chainA.GetSimApp().BankKeeper.SendCoinsFromAccountToModule(suite.chainA.GetContext(), refundAcc, types.ModuleName, fee.Total())

				expIdentifiedPacketFees = []types.IdentifiedPacketFees{identifiedPacketFee1, identifiedPacketFee2}
			}, true,
		},
		{
			"invalid refund acc address", func() {
				// store the fee in state & update escrow account balance
				packetID := channeltypes.NewPacketId(suite.path.EndpointA.ChannelConfig.PortID, suite.path.EndpointA.ChannelID, uint64(1))
				packetFees := types.NewPacketFees([]types.PacketFee{types.NewPacketFee(fee, "invalid refund address", nil)})
				identifiedPacketFees := types.NewIdentifiedPacketFees(packetID, packetFees.PacketFees)

				suite.chainA.GetSimApp().IBCFeeKeeper.SetFeesInEscrow(suite.chainA.GetContext(), packetID, packetFees)

				suite.chainA.GetSimApp().BankKeeper.SendCoinsFromAccountToModule(suite.chainA.GetContext(), refundAcc, types.ModuleName, fee.Total())

				expIdentifiedPacketFees = []types.IdentifiedPacketFees{identifiedPacketFees}
			}, false,
		},
		{
			"distributing to blocked address is skipped", func() {
				blockedAddr := suite.chainA.GetSimApp().AccountKeeper.GetModuleAccount(suite.chainA.GetContext(), transfertypes.ModuleName).GetAddress().String()

				// store the fee in state & update escrow account balance
				packetID := channeltypes.NewPacketId(suite.path.EndpointA.ChannelConfig.PortID, suite.path.EndpointA.ChannelID, uint64(1))
				packetFees := types.NewPacketFees([]types.PacketFee{types.NewPacketFee(fee, blockedAddr, nil)})
				identifiedPacketFees := types.NewIdentifiedPacketFees(packetID, packetFees.PacketFees)

				suite.chainA.GetSimApp().IBCFeeKeeper.SetFeesInEscrow(suite.chainA.GetContext(), packetID, packetFees)

				suite.chainA.GetSimApp().BankKeeper.SendCoinsFromAccountToModule(suite.chainA.GetContext(), refundAcc, types.ModuleName, fee.Total())

				expIdentifiedPacketFees = []types.IdentifiedPacketFees{identifiedPacketFees}

				expEscrowBal = fee.Total()
				expRefundBal = expRefundBal.Sub(fee.Total())
			}, true,
		},
	}

	for _, tc := range testCases {
		tc := tc

		suite.Run(tc.name, func() {
			suite.SetupTest()                   // reset
			suite.coordinator.Setup(suite.path) // setup channel
			expIdentifiedPacketFees = []types.IdentifiedPacketFees{}
			expEscrowBal = sdk.Coins{}
			locked = false

			// setup
			refundAcc = suite.chainA.SenderAccount.GetAddress()
			moduleAcc := suite.chainA.GetSimApp().IBCFeeKeeper.GetFeeModuleAddress()

			// expected refund balance if the refunds are successful
			// NOTE: tc.malleate() should transfer from refund balance to correctly set the escrow balance
			expRefundBal = suite.chainA.GetSimApp().BankKeeper.GetAllBalances(suite.chainA.GetContext(), refundAcc)

			fee = types.Fee{
				RecvFee:    defaultReceiveFee,
				AckFee:     defaultAckFee,
				TimeoutFee: defaultTimeoutFee,
			}

			tc.malleate()

			// refundAcc balance before distribution
			originalRefundBal := suite.chainA.GetSimApp().BankKeeper.GetAllBalances(suite.chainA.GetContext(), refundAcc)
			originalEscrowBal := suite.chainA.GetSimApp().BankKeeper.GetAllBalances(suite.chainA.GetContext(), moduleAcc)

			err := suite.chainA.GetSimApp().IBCFeeKeeper.RefundFeesOnChannelClosure(suite.chainA.GetContext(), suite.path.EndpointA.ChannelConfig.PortID, suite.path.EndpointA.ChannelID)

			// refundAcc balance after RefundFeesOnChannelClosure
			refundBal := suite.chainA.GetSimApp().BankKeeper.GetAllBalances(suite.chainA.GetContext(), refundAcc)
			escrowBal := suite.chainA.GetSimApp().BankKeeper.GetAllBalances(suite.chainA.GetContext(), moduleAcc)

			if tc.expPass {
				suite.Require().NoError(err)
			} else {
				suite.Require().Error(err)
			}

			suite.Require().Equal(locked, suite.chainA.GetSimApp().IBCFeeKeeper.IsLocked(suite.chainA.GetContext()))

			if locked || !tc.expPass {
				// refund account and escrow account balances should remain unchanged
				suite.Require().Equal(originalRefundBal, refundBal)
				suite.Require().Equal(originalEscrowBal, escrowBal)

				// ensure none of the fees were deleted
				suite.Require().Equal(expIdentifiedPacketFees, suite.chainA.GetSimApp().IBCFeeKeeper.GetIdentifiedPacketFeesForChannel(suite.chainA.GetContext(), suite.path.EndpointA.ChannelConfig.PortID, suite.path.EndpointA.ChannelID))
			} else {
				suite.Require().Equal(expEscrowBal, escrowBal) // escrow balance should be empty
				suite.Require().Equal(expRefundBal, refundBal) // all packets should have been refunded

				// all fees in escrow should be deleted for this channel
				suite.Require().Empty(suite.chainA.GetSimApp().IBCFeeKeeper.GetIdentifiedPacketFeesForChannel(suite.chainA.GetContext(), suite.path.EndpointA.ChannelConfig.PortID, suite.path.EndpointA.ChannelID))
			}

		})
	}
}